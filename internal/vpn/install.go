package vpn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/sysproc"
)

// install.go installs the external clients the VPN drivers need, per OS: the
// VPN page offers an Install button wherever Missing() reports binaries; the
// plan below maps driver kind + mode to the platform's package manager.

// InstallCommand is one step of an install plan.
type InstallCommand struct {
	Argv    []string `json:"argv"`
	Elevate bool     `json:"elevate"` // run through the configured elevation (sudo/pkexec)
	Note    string   `json:"note,omitempty"`
}

// InstallPlan says whether and how a driver mode can be installed here.
type InstallPlan struct {
	Installable bool             `json:"installable"`
	Note        string           `json:"note,omitempty"`     // why not, or extra guidance
	Commands    []InstallCommand `json:"commands,omitempty"` // the exact steps, shown before running
	Manual      []string         `json:"manual,omitempty"`   // copyable commands when not installable
}

// package manager families a Linux distribution can use.
type pkgManager struct {
	bins  []string // the package manager binary (first is what runs)
	args  []string // fixed args before the package names
	root  bool     // needs elevation
	label string   // "apt-get install", for notes
}

var packageManagers = map[string]pkgManager{
	"apt":    {bins: []string{"apt-get"}, args: []string{"install", "-y"}, root: true, label: "apt-get install"},
	"dnf":    {bins: []string{"dnf"}, args: []string{"install", "-y"}, root: true, label: "dnf install"},
	"pacman": {bins: []string{"pacman"}, args: []string{"-S", "--needed"}, root: true, label: "pacman -S"},
	"zypper": {bins: []string{"zypper"}, args: []string{"install", "-y"}, root: true, label: "zypper install"},
}

// wingetIDs maps a package to its winget id (Windows).
var wingetIDs = map[string]string{
	"wireguard-tools": "WireGuard.WireGuard",
	"openvpn":         "OpenVPN.OpenVPN",
}

// packages maps driver kind + mode to the packages to install; nil = the
// mode needs no installable package (in-process drivers).
func packages(kind, mode string) []string {
	switch kind {
	case "WireGuard":
		if mode == ModeSystem {
			return []string{"wireguard-tools"}
		}
	case "OpenVPN":
		return []string{"openvpn"}
	case "OpenConnect":
		if mode == ModeUserspace {
			return []string{"openconnect", "ocproxy"}
		}
		return []string{"openconnect"}
	case "OpenFortiVPN":
		return []string{"openfortivpn"}
	}
	return nil
}

// osReleasePath is where Linux distributions describe themselves; lookPath
// and goos are variables so tests can pin the platform.
var (
	osReleasePath = "/etc/os-release"
	lookPath      = exec.LookPath
)

// linuxPM picks the package manager family from /etc/os-release (ID first,
// then ID_LIKE); "" when the distribution is unknown.
func linuxPM() (pkgManager, bool) {
	release := map[string]string{}
	if b, err := os.ReadFile(osReleasePath); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			release[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	ids := []string{}
	if id := release["ID"]; id != "" {
		ids = append(ids, id)
	}
	ids = append(ids, strings.Fields(release["ID_LIKE"])...)
	for _, id := range ids {
		switch {
		case id == "debian" || id == "ubuntu" || strings.Contains(id, "debian"):
			return packageManagers["apt"], true
		case id == "fedora" || strings.Contains(id, "rhel") || id == "centos":
			return packageManagers["dnf"], true
		case id == "arch" || strings.Contains(id, "arch"):
			return packageManagers["pacman"], true
		case strings.Contains(id, "suse") || id == "sles":
			return packageManagers["zypper"], true
		}
	}
	return pkgManager{}, false
}

// PlanInstall says how to install what Missing(kind, mode) reports.
func PlanInstall(kind, mode string, elevate []string) (InstallPlan, error) {
	d, ok := drivers[kind]
	if !ok {
		return InstallPlan{}, errors.New("unknown VPN type " + kind)
	}
	valid := false
	for _, m := range d.Modes {
		if m == mode {
			valid = true
		}
	}
	if !valid {
		return InstallPlan{}, errors.New(d.Label + " has no " + mode + " mode")
	}
	if Missing(d, nil, mode, elevate) == "" {
		return InstallPlan{Installable: false, Note: "everything this mode needs is already installed"}, nil
	}
	pkgs := packages(kind, mode)
	if len(pkgs) == 0 {
		return InstallPlan{Installable: false, Note: d.Label + " " + mode + " mode runs inside opsdeck — nothing to install"}, nil
	}
	return planOn(runtime.GOOS, pkgs, kind, mode, elevate), nil
}

// planOn builds the plan for a pinned operating system (tests pass "linux",
// "darwin", "windows" explicitly).
func planOn(goos string, pkgs []string, kind, mode string, elevate []string) InstallPlan {
	switch goos {
	case "darwin":
		return darwinPlan(pkgs, kind, mode)
	case "windows":
		return windowsPlan(pkgs, kind, mode)
	case "linux":
		return linuxPlan(pkgs, kind, mode, elevate)
	}
	return InstallPlan{Installable: false, Manual: []string{"install " + strings.Join(pkgs, ", ") + " with your package manager"}, Note: "this operating system has no automatic install"}
}

func darwinPlan(pkgs []string, kind, mode string) InstallPlan {
	if _, err := lookPath("brew"); err != nil {
		return InstallPlan{Installable: false, Manual: []string{"brew install " + strings.Join(pkgs, " ")},
			Note: "install Homebrew first (https://brew.sh), then install from here"}
	}
	plan := InstallPlan{Installable: true, Commands: []InstallCommand{{Argv: append([]string{"brew", "install"}, pkgs...)}}}
	if kind == "OpenFortiVPN" {
		plan.Commands[0].Note = "openfortivpn needs a recent version (one with --cookie-on-stdin)"
	}
	return plan
}

func windowsPlan(pkgs []string, kind, mode string) InstallPlan {
	var ids []string
	for _, p := range pkgs {
		if id, ok := wingetIDs[p]; ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return InstallPlan{Installable: false,
			Note: "this client has no official Windows package; WireGuard, Tailscale and NetBird work here in userspace mode without extra binaries"}
	}
	cmds := []InstallCommand{}
	for _, id := range ids {
		cmds = append(cmds, InstallCommand{Argv: []string{"winget", "install", "-e", "--id", id, "--accept-source-agreements", "--accept-package-agreements"}})
	}
	return InstallPlan{Installable: true, Commands: cmds}
}

func linuxPlan(pkgs []string, kind, mode string, elevate []string) InstallPlan {
	pm, ok := linuxPM()
	if !ok {
		return InstallPlan{Installable: false,
			Manual: []string{"install " + strings.Join(pkgs, ", ") + " with your distribution's package manager"},
			Note:   "unrecognized distribution — install the client manually and reload this page"}
	}
	if pm.root && len(elevate) == 0 {
		return InstallPlan{Installable: false,
			Note: "set \"Run system VPNs with\" to sudo or pkexec in App settings first (the install needs root)"}
	}
	plan := InstallPlan{Installable: true}
	installs := pkgs
	if pm.bins[0] != "apt-get" {
		// ocproxy is only packaged for Debian families; elsewhere it is built
		// from source (the note says so).
		installs = make([]string, 0, len(pkgs))
		for _, p := range pkgs {
			if p != "ocproxy" {
				installs = append(installs, p)
			}
		}
	}
	if pm.bins[0] == "apt-get" {
		plan.Commands = append(plan.Commands, InstallCommand{Argv: []string{"apt-get", "update"}, Elevate: true, Note: "refresh the package lists"})
	}
	plan.Commands = append(plan.Commands, InstallCommand{Argv: append(append([]string{pm.bins[0]}, pm.args...), installs...), Elevate: pm.root})
	if kind == "OpenFortiVPN" {
		plan.Commands[len(plan.Commands)-1].Note = "openfortivpn needs a recent version (one with --cookie-on-stdin; Ubuntu 24.04 or newer)"
	}
	if kind == "OpenConnect" && mode == ModeUserspace && pm.bins[0] != "apt-get" {
		plan.Note = "ocproxy is not packaged for this distribution: install openconnect here, ocproxy from its upstream source, then reload this page"
	}
	return plan
}

// ---------------------------------------------------------------- runner

// InstallStatus is the state of the single install job.
type InstallStatus struct {
	Running bool     `json:"running"`
	Kind    string   `json:"kind,omitempty"`
	Mode    string   `json:"mode,omitempty"`
	Log     []string `json:"log,omitempty"`
	Err     string   `json:"err,omitempty"`
	Done    bool     `json:"done"`
}

var (
	installMu sync.Mutex
	install   InstallStatus
)

// StartInstall runs an install plan in the background; one job at a time.
func StartInstall(kind, mode string, elevate []string) error {
	installMu.Lock()
	defer installMu.Unlock()
	if install.Running {
		return errors.New("an install is already running (" + install.Kind + " " + install.Mode + ")")
	}
	plan, err := PlanInstall(kind, mode, elevate)
	if err != nil {
		return err
	}
	if !plan.Installable {
		return errors.New(plan.Note)
	}
	install = InstallStatus{Running: true, Kind: kind, Mode: mode}
	go runInstall(plan, elevate)
	return nil
}

// GetInstallStatus copies the job state.
func GetInstallStatus() InstallStatus {
	installMu.Lock()
	defer installMu.Unlock()
	out := install
	out.Log = append([]string(nil), install.Log...)
	return out
}

func appendLog(line string) {
	install.Log = append(install.Log, line)
	if len(install.Log) > 500 { // keep the tail: package managers are chatty
		install.Log = append([]string{"… (older lines dropped)"}, install.Log[len(install.Log)-499:]...)
	}
}

// runInstall executes the plan's commands in order, stopping at the first
// failure. It runs on its own goroutine and owns the install state until done.
func runInstall(plan InstallPlan, elevate []string) {
	defer func() {
		installMu.Lock()
		install.Running = false
		install.Done = true
		installMu.Unlock()
	}()
	for _, c := range plan.Commands {
		argv, env := c.Argv, []string(nil)
		if c.Elevate {
			argv, env = elevatedCommand(argv, elevate)
		}
		installMu.Lock()
		appendLog("$ " + strings.Join(argv, " "))
		installMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		cmd := sysproc.CommandContext(ctx, argv[0], argv[1:]...)
		if env != nil {
			cmd.Env = env
		}
		out, err := cmd.CombinedOutput()
		cancel()
		if n := len(out); n > 0 {
			installMu.Lock()
			for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
				appendLog(line)
			}
			if hint := sudoHintOf(out); hint != "" {
				appendLog(hint)
			}
			installMu.Unlock()
		}
		if err != nil {
			installMu.Lock()
			install.Err = fmt.Sprintf("%s failed: %v", argv[0], err)
			installMu.Unlock()
			return
		}
	}
	installMu.Lock()
	appendLog("done — the VPN page shows what is ready after a reload")
	installMu.Unlock()
}

// sudoHintOf returns the first sudo explanation in a command's output
// ("" = none); the install log shows it next to the raw output.
func sudoHintOf(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		if hint := sudoHint(line); hint != "" {
			return hint
		}
	}
	return ""
}
