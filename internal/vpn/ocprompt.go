package vpn

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/vault"
)

// Questions openconnect asks while it logs in with a password. With
// --non-inter and --passwd-on-stdin it reads further answers from stdin,
// which opsdeck keeps open and answers itself:
//
//   - a choice (GROUP: [a|b|c], a GlobalProtect gateway): the profile's group
//     when openconnect did not match it itself, otherwise the user picks one
//     (Config.Code with Choices);
//   - a one-time code (a second password, a RADIUS challenge "Answer:"):
//     the login's TOTP code the first time, otherwise the code the user
//     enters or that arrives from the phone, like FortiGate codes;
//   - the password again when the gateway starts over after a wrong code.
//
// A question it cannot answer closes stdin: openconnect gives up, and the
// tunnel fails with the reason. The TOTP secret is never on its command line.

var (
	ocChoiceRe   = regexp.MustCompile(`^(.*?)\s*\[(.+)\]:$`)
	ocCodeRe     = regexp.MustCompile(`(?i)code|token|otp|passcode|\bpin\b|answer|response|second|challenge|verif|کد|یکبار`)
	ocPasswordRe = regexp.MustCompile(`(?i)pass|رمز`)
	ocUserRe     = regexp.MustCompile(`(?i)^(user|login|e-?mail|account)`)
	ocErrorRe    = regexp.MustCompile(`(?i)fail|invalid|incorrect|denied|wrong|rejected|expired|not accepted`)
	// openconnect's own progress, not the gateway's message
	ocProgressRe = regexp.MustCompile(`^Connected to |^SSL negotiation|^Got HTTP response|^Unexpected \d+ result|^XML POST enabled|^Attempting to connect|^Server certificate|^Auth choice |^\*\*\*$`)
)

// ocMaxQuestions bounds a login that keeps asking.
const ocMaxQuestions = 12

// ocAnswers answers openconnect's questions during one login.
type ocAnswers struct {
	cfg            *Config
	ctx            context.Context
	user, password string
	group          string // the profile's group ("" = none)
	totp           string // the login's TOTP secret ("" = none)

	t *cliTunnel

	mu        sync.Mutex
	loggedIn  bool   // the login is over: output is no question any more
	msg       string // the gateway's last message
	failed    bool   // the gateway reported an error since the last answer
	questions int
	codes     int    // one-time codes sent
	codeLabel string // the question the last code answered
	restarted bool   // the gateway started over after a code
}

// line takes a line of output: the gateway's messages and errors.
func (a *ocAnswers) line(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.HasPrefix(s, "POST ") || strings.HasPrefix(s, "GET ") { // the next form
		a.msg, a.failed = "", false
		return
	}
	if strings.HasPrefix(s, "Got CONNECT response") || ocConnectedRe.MatchString(s) {
		a.loggedIn = true
	}
	if ocProgressRe.MatchString(s) {
		return
	}
	a.msg = s
	if ocErrorRe.MatchString(s) {
		a.failed = true
	}
}

// question answers a question of openconnect (without blocking its output).
func (a *ocAnswers) question(q string) {
	a.mu.Lock()
	if a.loggedIn {
		a.mu.Unlock()
		return
	}
	msg, failed := a.msg, a.failed
	a.msg, a.failed = "", false
	a.questions++
	n := a.questions
	a.mu.Unlock()
	go func() {
		ans, err := a.answer(q, msg, failed, n)
		<-a.t.started
		if err == nil {
			err = a.t.p.answer(ans)
		}
		if err != nil {
			a.t.failWith(err.Error())
			a.t.p.noAnswer()
		}
	}()
}

func (a *ocAnswers) answer(q, msg string, failed bool, n int) (string, error) {
	if n > ocMaxQuestions {
		return "", fmt.Errorf("the gateway keeps asking (%s); see the log", q)
	}
	log := a.cfg.Log
	if m := ocChoiceRe.FindStringSubmatch(q); m != nil {
		label := strings.TrimSpace(strings.TrimSuffix(m[1], ":"))
		choices := strings.Split(m[2], "|")
		if a.group != "" {
			log.Printf("the gateway offers no %s %q (it offers %s)", label, a.group, strings.Join(choices, ", "))
		}
		if a.cfg.Code == nil {
			return "", fmt.Errorf("the gateway asks for the %s (%s): set the profile's group to one of them", label, strings.Join(choices, ", "))
		}
		log.Printf("the gateway asks for the %s; asking which one", label)
		ans, err := a.cfg.Code(a.ctx, CodePrompt{Network: a.cfg.Res.Name, Message: msg, Label: label, Choices: choices, Since: time.Now()})
		if err != nil {
			return "", err
		}
		for _, c := range choices {
			if strings.EqualFold(strings.TrimSpace(ans), c) {
				log.Printf("%s %s (set it as the profile's group to skip this question)", label, c)
				return c, nil
			}
		}
		return "", fmt.Errorf("%q is not one of the gateway's choices (%s)", ans, strings.Join(choices, ", "))
	}
	label := strings.TrimSpace(strings.TrimSuffix(q, ":"))
	code, password := ocCodeRe.MatchString(label), ocPasswordRe.MatchString(label)
	a.mu.Lock()
	codesSent := a.codes > 0
	a.mu.Unlock()
	switch {
	case !code && !password && ocUserRe.MatchString(label):
		if a.user == "" {
			return "", errors.New("the gateway asks for a user name: set it on the profile's login")
		}
		return a.user, nil
	case !code && password && failed && !codesSent:
		return "", errors.New("login failed: check the username and password of the profile's login")
	case !code && password && failed:
		// a wrong code: the gateway starts over
		log.Printf("the gateway did not accept the code and starts over; sending the password again")
		a.mu.Lock()
		a.restarted = true
		a.mu.Unlock()
		a.t.failWith("") // its "Login failed" was about the code
		return a.password, nil
	}
	// a second password (after the first) or an answer: a one-time code
	return a.code(label, msg, failed, code || password)
}

// code answers a request for a one-time code: with the login's TOTP code the
// first time, otherwise from the user.
func (a *ocAnswers) code(label, msg string, failed, totpOK bool) (string, error) {
	a.mu.Lock()
	attempt := a.codes
	retry := attempt > 0 && (failed || a.restarted || label == a.codeLabel)
	a.codes++
	a.codeLabel, a.restarted = label, false
	a.mu.Unlock()
	if attempt == 3 {
		return "", errors.New("the gateway did not accept the one-time code (3 attempts); connect again for a new one")
	}
	log := a.cfg.Log
	if attempt == 0 && totpOK && a.totp != "" {
		code, _, _, err := vault.TOTP(a.totp, time.Now())
		if err == nil {
			log.Printf("the gateway asks for a one-time code; sending the login's TOTP code")
			return code, nil
		}
		log.Printf("the login's TOTP secret: %v", err)
	}
	what := firstNonEmpty(msg, label)
	if retry {
		log.Printf("the gateway did not accept the code; asking again")
	} else {
		log.Printf("the gateway asks for a one-time code (%s): %s", label, firstNonEmpty(msg, "no message"))
	}
	if a.cfg.Code == nil {
		return "", fmt.Errorf("the gateway asks for a one-time code (%s), which this connection cannot ask for", what)
	}
	c, err := a.cfg.Code(a.ctx, CodePrompt{Network: a.cfg.Res.Name, Message: msg, Label: label, Retry: retry, Since: time.Now()})
	if err != nil {
		return "", err
	}
	if c = NormalizeCode(c); c == "" {
		return "", errors.New("no one-time code was entered")
	}
	log.Printf("sending the one-time code")
	return c, nil
}
