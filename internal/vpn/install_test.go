package vpn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planOn is pinned per operating system, so the matrix below tests every
// platform on every machine (including CI, which is Linux-only).

func TestInstallPlanMatrix(t *testing.T) {
	elevate := []string{"sudo", "-A"}
	origRelease := osReleasePath
	defer func() { osReleasePath = origRelease }()
	tests := []struct {
		name        string
		goos        string
		release     string // /etc/os-release content (linux only)
		kind, mode  string
		brew        bool // darwin: brew on PATH
		noElevate   bool // linux: no elevation configured
		installable bool
		wantArgs    []string // the last command's argv (package manager + packages)
		elevateLast bool
		notePart    string // a substring the plan's (or last command's) note says
	}{
		{
			name: "openconnect userspace on ubuntu: apt update + install openconnect ocproxy",
			goos: "linux", release: "ID=ubuntu\nID_LIKE=debian\n", kind: "OpenConnect", mode: ModeUserspace,
			installable: true, elevateLast: true,
			wantArgs: []string{"apt-get", "install", "-y", "openconnect", "ocproxy"},
		},
		{
			name: "openconnect userspace on fedora: dnf with an ocproxy note",
			goos: "linux", release: "ID=fedora\n", kind: "OpenConnect", mode: ModeUserspace,
			installable: true, elevateLast: true,
			wantArgs: []string{"dnf", "install", "-y", "openconnect"},
			notePart: "ocproxy is not packaged",
		},
		{
			name: "openfortivpn on arch: pacman with a version note",
			goos: "linux", release: "ID=arch\nID_LIKE=arch\n", kind: "OpenFortiVPN", mode: ModeSystem,
			installable: true, elevateLast: true,
			wantArgs: []string{"pacman", "-S", "--needed", "openfortivpn"},
			notePart: "cookie-on-stdin",
		},
		{
			name: "wireguard system on debian via ID_LIKE",
			goos: "linux", release: "ID=linuxmint\nID_LIKE=\"debian debian12\"\n", kind: "WireGuard", mode: ModeSystem,
			installable: true, elevateLast: true,
			wantArgs: []string{"apt-get", "install", "-y", "wireguard-tools"},
		},
		{
			name: "unknown distribution: manual commands only",
			goos: "linux", release: "ID=genericos\n", kind: "OpenVPN", mode: ModeSystem,
			installable: false, notePart: "unrecognized distribution",
		},
		{
			name: "linux without elevation set: asks for settings first",
			goos: "linux", release: "ID=ubuntu\n", kind: "OpenVPN", mode: ModeSystem, noElevate: true,
			installable: false, notePart: "Run system VPNs with",
		},
		{
			name: "openvpn on macos: brew, no elevation",
			goos: "darwin", kind: "OpenVPN", mode: ModeSystem, brew: true,
			installable: true, elevateLast: false,
			wantArgs: []string{"brew", "install", "openvpn"},
		},
		{
			name: "openfortivpn on macos: brew with a version note",
			goos: "darwin", kind: "OpenFortiVPN", mode: ModeSystem, brew: true,
			installable: true, elevateLast: false,
			wantArgs: []string{"brew", "install", "openfortivpn"},
			notePart: "cookie-on-stdin",
		},
		{
			name: "macos without homebrew: manual commands",
			goos: "darwin", kind: "OpenConnect", mode: ModeUserspace, brew: false,
			installable: false, notePart: "Homebrew",
		},
		{
			name: "openvpn on windows: winget id",
			goos: "windows", kind: "OpenVPN", mode: ModeSystem,
			installable: true, elevateLast: false,
			wantArgs: []string{"winget", "install", "-e", "--id", "OpenVPN.OpenVPN", "--accept-source-agreements", "--accept-package-agreements"},
		},
		{
			name: "openconnect on windows: no package, userspace hint",
			goos: "windows", kind: "OpenConnect", mode: ModeUserspace,
			installable: false, notePart: "userspace mode",
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			osReleasePath = filepath.Join(t.TempDir(), "os-release")
			if c.release != "" {
				if err := os.WriteFile(osReleasePath, []byte(c.release), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			origLook := lookPath
			lookPath = func(bin string) (string, error) {
				if bin == "brew" && c.brew {
					return "/usr/local/bin/brew", nil
				}
				return "", &os.PathError{}
			}
			defer func() { lookPath = origLook }()

			pkgs := packages(c.kind, c.mode)
			elev := elevate
			if c.noElevate {
				elev = nil
			}
			plan := planOn(c.goos, pkgs, c.kind, c.mode, elev)
			if plan.Installable != c.installable {
				t.Fatalf("installable = %v, want %v (note: %q)", plan.Installable, c.installable, plan.Note)
			}
			if c.notePart != "" {
				note := plan.Note + lastCommandNote(plan)
				if !strings.Contains(note, c.notePart) {
					t.Fatalf("note %q misses %q", note, c.notePart)
				}
			}
			if !c.installable {
				return
			}
			last := plan.Commands[len(plan.Commands)-1]
			if strings.Join(last.Argv, " ") != strings.Join(c.wantArgs, " ") {
				t.Fatalf("last command = %v, want %v", last.Argv, c.wantArgs)
			}
			if last.Elevate != c.elevateLast {
				t.Fatalf("last command elevate = %v, want %v", last.Elevate, c.elevateLast)
			}
		})
	}
}

func lastCommandNote(p InstallPlan) string {
	if len(p.Commands) == 0 {
		return ""
	}
	return p.Commands[len(p.Commands)-1].Note
}

// PlanInstall consults Missing first: a mode whose binaries are already on
// PATH is reported as installed, and embedded drivers are never installable.
func TestPlanInstallAlreadyInstalledAndEmbedded(t *testing.T) {
	origLook := lookPath
	lookPath = func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
	defer func() { lookPath = origLook }()

	plan, err := PlanInstall("OpenConnect", ModeUserspace, []string{"sudo", "-A"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Installable {
		t.Fatalf("openconnect found on PATH: plan says installable")
	}
	if !strings.Contains(plan.Note, "already installed") {
		t.Fatalf("note = %q", plan.Note)
	}

	// NetBird system mode runs inside opsdeck: with no privileges Missing
	// reports the CAP_NET_ADMIN message, and the plan explains there is
	// nothing to install.
	plan, err = PlanInstall("NetBird", ModeSystem, []string{"sudo", "-A"})
	if err != nil {
		t.Fatal(err)
	}
	// non-root: Missing reports the CAP_NET_ADMIN message and the plan
	// explains there is nothing to install; as root it is "already installed".
	// Either way there is nothing to download.
	if plan.Installable || (!strings.Contains(plan.Note, "inside opsdeck") && !strings.Contains(plan.Note, "already installed")) {
		t.Fatalf("netbird plan = %+v", plan)
	}

	if _, err := PlanInstall("Nope", ModeSystem, nil); err == nil {
		t.Fatal("unknown kind must error")
	}
}

func TestPackagesMapping(t *testing.T) {
	tests := []struct {
		kind, mode string
		want       []string
	}{
		{"WireGuard", ModeUserspace, nil}, // in-process wireguard-go
		{"WireGuard", ModeSystem, []string{"wireguard-tools"}},
		{"OpenVPN", ModeSystem, []string{"openvpn"}},
		{"OpenConnect", ModeUserspace, []string{"openconnect", "ocproxy"}},
		{"OpenConnect", ModeSystem, []string{"openconnect"}},
		{"OpenFortiVPN", ModeSystem, []string{"openfortivpn"}},
		{"Tailscale", ModeUserspace, nil}, // tsnet
		{"NetBird", ModeSystem, nil},      // embedded lib
	}
	for _, c := range tests {
		if got := packages(c.kind, c.mode); strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("packages(%s, %s) = %v, want %v", c.kind, c.mode, got, c.want)
		}
	}
}
