// Package update finds new opsdeck releases on GitHub and installs them: the
// desktop app with its installer or package (Windows installer, macOS app
// bundle, .deb/.rpm through pkexec), the CLI by replacing its binary. Every
// download is checked against the release's SHA256SUMS.txt.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Repo is where releases are published.
const Repo = "vahidid/opsdeck"

// SumsFile is the release asset with the SHA-256 of every other asset.
const SumsFile = "SHA256SUMS.txt"

// Kind is how this copy of opsdeck was installed.
type Kind string

const (
	KindCLI     Kind = "cli"     // the opsdeck binary
	KindDesktop Kind = "desktop" // the desktop app (installer, app bundle or package)
)

// Release is a published GitHub release.
type Release struct {
	Version   string    `json:"version"`
	Name      string    `json:"name"`
	Notes     string    `json:"notes"`
	URL       string    `json:"url"`
	Published time.Time `json:"published"`
	Assets    []Asset   `json:"-"`
}

// Asset is a file of a release.
type Asset struct {
	Name string
	URL  string
	Size int64
}

// Status is what the UI shows.
type Status struct {
	Current   string    `json:"current"`
	Latest    *Release  `json:"latest,omitempty"`
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	Error     string    `json:"error,omitempty"`
	// Method is how the update installs here: installer or portable
	// (Windows), app (macOS), deb, rpm, binary (the CLI). Empty: download it by hand, for
	// the reason in Manual.
	Method string `json:"method,omitempty"`
	Manual string `json:"manual,omitempty"`
	// State of an install: downloading, installing, restarting, done, failed.
	State    string  `json:"state,omitempty"`
	Progress float64 `json:"progress"`
	Message  string  `json:"message,omitempty"`
}

// Updater checks for and installs updates of the running opsdeck.
type Updater struct {
	Current string
	Kind    Kind
	// Quit ends the process so the installer can replace it (desktop app);
	// nil for the CLI, which asks to be restarted.
	Quit func()

	// for tests
	API    string // GitHub API base
	Exe    string // running executable
	GOOS   string
	GOARCH string
	Client *http.Client
	run    runner

	mu     sync.Mutex
	status Status
	latest *Release
	busy   bool
}

// CheckEvery is how long a check result is reused.
const CheckEvery = time.Hour

// New returns an updater for this process.
func New(current string, kind Kind, quit func()) *Updater {
	u := &Updater{Current: current, Kind: kind, Quit: quit}
	u.init()
	// what a replaced Windows binary left behind
	os.Remove(u.Exe + ".old")
	return u
}

func (u *Updater) init() {
	if u.API == "" {
		// OPSDECK_UPDATE_API: a stand-in for GitHub when trying updates
		u.API = os.Getenv("OPSDECK_UPDATE_API")
	}
	if u.API == "" {
		u.API = "https://api.github.com"
	}
	if u.GOOS == "" {
		u.GOOS, u.GOARCH = runtime.GOOS, runtime.GOARCH
	}
	if u.Exe == "" {
		if exe, err := os.Executable(); err == nil {
			if r, err := filepath.EvalSymlinks(exe); err == nil {
				exe = r
			}
			u.Exe = exe
		}
	}
	if u.Client == nil {
		u.Client = &http.Client{Timeout: 30 * time.Minute}
	}
	if u.run == nil {
		u.run = osRunner{}
	}
}

// Status returns the last check, checking again when it is older than
// CheckEvery (or always with refresh). check=false only reports.
func (u *Updater) Status(ctx context.Context, check, refresh bool) Status {
	u.mu.Lock()
	u.init()
	stale := u.status.CheckedAt.IsZero() || time.Since(u.status.CheckedAt) > CheckEvery
	busy := u.busy
	u.mu.Unlock()
	if check && !busy && (refresh || stale) {
		rel, err := u.fetchLatest(ctx)
		u.mu.Lock()
		u.status.CheckedAt = time.Now()
		u.status.Error = ""
		if err != nil {
			u.status.Error = err.Error()
		} else {
			u.latest = rel
		}
		u.mu.Unlock()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	st := u.status
	st.Current = u.Current
	st.Latest = u.latest
	st.Available = u.latest != nil && Newer(u.Current, u.latest.Version)
	st.Method, st.Manual = "", ""
	if st.Available {
		if p := u.plan(u.latest); p.method != "" {
			st.Method = p.method
		} else {
			st.Manual = p.manual
		}
	} else if u.latest != nil && !isVersion(u.Current) {
		st.Manual = "this is a development build (" + u.Current + "); install a release to get updates"
	}
	return st
}

func isVersion(s string) bool { _, ok := parseVersion(s); return ok }

func (u *Updater) fetchLatest(ctx context.Context) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", u.API+"/repos/"+Repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "opsdeck/"+u.Current)
	res, err := u.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("checking for updates: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, errors.New("no release published yet")
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("checking for updates: GitHub answered %s", res.Status)
	}
	var in struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&in); err != nil {
		return nil, fmt.Errorf("checking for updates: %w", err)
	}
	rel := &Release{Version: strings.TrimPrefix(in.TagName, "v"), Name: in.Name, Notes: in.Body, URL: in.HTMLURL, Published: in.PublishedAt}
	for _, a := range in.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size})
	}
	return rel, nil
}

// plan is how to install a release here.
type plan struct {
	method string // "" = by hand, see manual
	manual string
	asset  *Asset
	target string // the file or app bundle that is replaced
}

func (u *Updater) plan(rel *Release) plan {
	pick := func(match func(string) bool) *Asset {
		for i := range rel.Assets {
			if match(rel.Assets[i].Name) {
				return &rel.Assets[i]
			}
		}
		return nil
	}
	hand := func(why string) plan { return plan{manual: why} }
	if u.Exe == "" {
		return hand("the location of this program is unknown")
	}
	if u.Kind == KindCLI {
		suffix := "-" + u.GOOS + "-" + u.GOARCH
		if u.GOOS == "windows" {
			suffix += ".exe"
		}
		a := pick(func(n string) bool { return strings.HasPrefix(n, "opsdeck-cli-") && strings.HasSuffix(n, suffix) })
		if a == nil {
			return hand("the release has no opsdeck binary for " + u.GOOS + "/" + u.GOARCH)
		}
		if !writable(filepath.Dir(u.Exe)) {
			return hand("no permission to replace " + u.Exe)
		}
		return plan{method: "binary", asset: a, target: u.Exe}
	}
	switch u.GOOS {
	case "windows":
		if u.GOARCH != "amd64" {
			return hand("the release has no Windows build for " + u.GOARCH)
		}
		// the installer leaves an uninstaller next to the app; otherwise
		// this is the portable .exe
		if _, err := os.Stat(filepath.Join(filepath.Dir(u.Exe), "uninstall.exe")); err == nil {
			if a := pick(func(n string) bool { return strings.HasSuffix(n, "-windows-x64-setup.exe") }); a != nil {
				return plan{method: "installer", asset: a, target: u.Exe}
			}
			return hand("the release has no Windows installer")
		}
		a := pick(func(n string) bool { return strings.HasSuffix(n, "-windows-x64-portable.exe") })
		if a == nil {
			return hand("the release has no portable Windows app")
		}
		if !writable(filepath.Dir(u.Exe)) {
			return hand("no permission to replace " + u.Exe)
		}
		return plan{method: "portable", asset: a, target: u.Exe}
	case "darwin":
		app := bundleOf(u.Exe)
		if app == "" {
			return hand("opsdeck is not running from an app bundle")
		}
		a := pick(func(n string) bool { return strings.HasSuffix(n, "-macos-universal.dmg") })
		if a == nil {
			return hand("the release has no macOS disk image")
		}
		if !writable(filepath.Dir(app)) {
			return hand("no permission to replace " + app)
		}
		return plan{method: "app", asset: a, target: app}
	case "linux":
		if _, err := u.run.lookPath("pkexec"); err != nil {
			return hand("installing a package needs pkexec (polkit)")
		}
		arch := map[string][2]string{"amd64": {"_amd64.deb", ".x86_64.rpm"}, "arm64": {"_arm64.deb", ".aarch64.rpm"}}[u.GOARCH]
		switch {
		case arch[0] == "":
		case u.run.owns("dpkg-query", "-S", u.Exe):
			if a := pick(func(n string) bool { return strings.HasSuffix(n, arch[0]) }); a != nil {
				return plan{method: "deb", asset: a, target: u.Exe}
			}
		case u.run.owns("rpm", "-qf", u.Exe):
			if a := pick(func(n string) bool { return strings.HasSuffix(n, arch[1]) }); a != nil {
				return plan{method: "rpm", asset: a, target: u.Exe}
			}
		default:
			return hand("opsdeck was not installed from a .deb or .rpm package")
		}
		return hand("the release has no package for this system")
	}
	return hand("updates are not supported on " + u.GOOS)
}

// bundleOf is the .app folder of a macOS executable (…/opsdeck.app/Contents/MacOS/opsdeck).
func bundleOf(exe string) string {
	dir := filepath.Dir(exe)
	if filepath.Base(dir) != "MacOS" || filepath.Base(filepath.Dir(dir)) != "Contents" {
		return ""
	}
	app := filepath.Dir(filepath.Dir(dir))
	if !strings.HasSuffix(app, ".app") {
		return ""
	}
	return app
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".opsdeck-write-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// Install downloads the latest release in the background and installs it;
// Status reports progress.
func (u *Updater) Install() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.init()
	if u.busy {
		return errors.New("an update is already being installed")
	}
	if u.latest == nil || !Newer(u.Current, u.latest.Version) {
		return errors.New("no update available")
	}
	p := u.plan(u.latest)
	if p.method == "" {
		return errors.New(p.manual)
	}
	u.busy = true
	u.status.State, u.status.Progress, u.status.Message = "downloading", 0, ""
	go u.install(u.latest, p)
	return nil
}

func (u *Updater) set(state, msg string) {
	u.mu.Lock()
	u.status.State, u.status.Message = state, msg
	if state == "failed" || state == "done" {
		u.busy = false
	}
	u.mu.Unlock()
}

func (u *Updater) install(rel *Release, p plan) {
	dir, err := os.MkdirTemp("", "opsdeck-update-")
	if err != nil {
		u.set("failed", err.Error())
		return
	}
	file := filepath.Join(dir, p.asset.Name)
	if err := u.download(rel, p.asset, file); err != nil {
		os.RemoveAll(dir)
		u.set("failed", err.Error())
		return
	}
	u.set("installing", "")
	restart, err := u.apply(p, file)
	if err != nil {
		u.set("failed", err.Error())
		return
	}
	if restart && u.Quit != nil {
		u.set("restarting", "opsdeck restarts with version "+rel.Version)
		time.Sleep(500 * time.Millisecond) // let the UI see it
		u.Quit()
		return
	}
	u.set("done", "Version "+rel.Version+" is installed. Restart opsdeck to use it.")
}

// download saves an asset and checks it against the release's checksums.
func (u *Updater) download(rel *Release, a *Asset, path string) error {
	var sums *Asset
	for i := range rel.Assets {
		if rel.Assets[i].Name == SumsFile {
			sums = &rel.Assets[i]
		}
	}
	if sums == nil {
		return errors.New("the release has no " + SumsFile + "; not installing an unverified file")
	}
	var list strings.Builder
	if err := u.get(sums.URL, &list, 0, nil); err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(list.String(), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == a.Name {
			want = strings.ToLower(f[0])
		}
	}
	if want == "" {
		return errors.New(SumsFile + " has no checksum for " + a.Name)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	err = u.get(a.URL, io.MultiWriter(f, h), a.Size, func(p float64) {
		u.mu.Lock()
		u.status.Progress = p
		u.mu.Unlock()
	})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s: checksum mismatch (got %s, want %s)", a.Name, got, want)
	}
	return nil
}

func (u *Updater) get(url string, w io.Writer, size int64, progress func(float64)) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "opsdeck/"+u.Current)
	res, err := u.Client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("downloading %s: %s", url, res.Status)
	}
	if size <= 0 {
		size = res.ContentLength
	}
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			done += int64(n)
			if progress != nil && size > 0 {
				progress(min(1, float64(done)/float64(size)))
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return fmt.Errorf("downloading: %w", rerr)
		}
	}
}

// apply installs the downloaded file; restart: the process must end (and is
// started again by the installer step).
func (u *Updater) apply(p plan, file string) (restart bool, err error) {
	switch p.method {
	case "binary":
		return false, replaceBinary(file, p.target, u.GOOS)
	case "portable":
		if err := replaceBinary(file, p.target, u.GOOS); err != nil {
			return false, err
		}
		return true, u.run.relaunchAfterExit(u.GOOS, p.method, "", p.target)
	case "deb", "rpm":
		if err := u.run.installPackage(p.method, file); err != nil {
			return false, err
		}
		return true, u.run.relaunchAfterExit(u.GOOS, p.method, "", p.target)
	case "installer", "app":
		return true, u.run.relaunchAfterExit(u.GOOS, p.method, file, p.target)
	}
	return false, errors.New("unknown install method " + p.method)
}

// replaceBinary puts file in place of target. A running executable can be
// renamed (Windows) or replaced (Unix), but not overwritten.
func replaceBinary(file, target, goos string) error {
	tmp := target + ".new"
	if err := copyFile(file, tmp, 0o755); err != nil {
		return err
	}
	if goos == "windows" {
		old := target + ".old"
		os.Remove(old)
		if err := os.Rename(target, old); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
