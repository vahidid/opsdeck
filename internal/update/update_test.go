package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		cur, latest string
		want        bool
	}{
		{"0.1.0", "0.1.1", true},
		{"v0.1.0", "v0.2.0", true},
		{"0.9.9", "1.0.0", true},
		{"0.10.0", "0.9.0", false},
		{"1.0.0", "1.0.0", false},
		{"1.0.0-rc.1", "1.0.0", true},
		{"1.0.0-rc.2", "1.0.0-rc.10", true},
		{"1.0.0", "1.0.0-rc.1", false},
		{"0.1.0-dev.abc1234", "0.1.0", true},
		{"9995b8f-dirty", "0.2.0", false},
		{"0.1.0", "garbage", false},
	} {
		if got := Newer(c.cur, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.cur, c.latest, got)
		}
	}
}

// fakeGitHub serves a release with the given files (name → content).
func fakeGitHub(t *testing.T, tag string, files map[string]string, badSum string) *httptest.Server {
	t.Helper()
	var ts *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		var sums strings.Builder
		type asset struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int    `json:"size"`
		}
		assets := []asset{}
		for name, body := range files {
			h := sha256.Sum256([]byte(body))
			sum := hex.EncodeToString(h[:])
			if name == badSum {
				sum = strings.Repeat("0", 64)
			}
			sums.WriteString(sum + "  " + name + "\n")
			assets = append(assets, asset{name, ts.URL + "/dl/" + name, len(body)})
		}
		files[SumsFile] = sums.String()
		assets = append(assets, asset{SumsFile, ts.URL + "/dl/" + SumsFile, sums.Len()})
		json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "name": "opsdeck " + tag, "body": "notes", "html_url": "https://example/rel", "assets": assets})
	})
	mux.HandleFunc("/dl/{name}", func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.PathValue("name")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(b))
	})
	ts = httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

type fakeRunner struct {
	mu        sync.Mutex
	owner     string // the package manager that owns the executable
	noPkexec  bool
	installed []string
	relaunch  [][4]string
}

func (f *fakeRunner) lookPath(n string) (string, error) {
	if n == "pkexec" && f.noPkexec {
		return "", os.ErrNotExist
	}
	return "/usr/bin/" + n, nil
}
func (f *fakeRunner) owns(name string, args ...string) bool { return name == f.owner }
func (f *fakeRunner) installPackage(method, file string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = append(f.installed, method+" "+filepath.Base(file))
	return nil
}
func (f *fakeRunner) relaunchAfterExit(goos, method, file, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.relaunch = append(f.relaunch, [4]string{goos, method, filepath.Base(file), target})
	return nil
}

func newTest(ts *httptest.Server, cur string, kind Kind, goos, exe string) (*Updater, *fakeRunner, *atomic.Int32) {
	quits := new(atomic.Int32)
	r := &fakeRunner{}
	u := &Updater{Current: cur, Kind: kind, API: ts.URL, Exe: exe, GOOS: goos, GOARCH: "amd64", run: r, Quit: func() { quits.Add(1) }}
	return u, r, quits
}

func wait(t *testing.T, u *Updater) Status {
	t.Helper()
	for i := 0; i < 200; i++ {
		st := u.Status(context.Background(), false, false)
		if st.State == "done" || st.State == "failed" || st.State == "restarting" {
			time.Sleep(600 * time.Millisecond) // the Quit after "restarting"
			return u.Status(context.Background(), false, false)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("install did not finish")
	return Status{}
}

var release = map[string]string{
	"opsdeck-cli-0.2.0-linux-amd64":          "new cli",
	"opsdeck-cli-0.2.0-windows-amd64.exe":    "new cli exe",
	"opsdeck_0.2.0_amd64.deb":                "deb",
	"opsdeck-0.2.0-1.x86_64.rpm":             "rpm",
	"opsdeck-0.2.0-windows-x64-setup.exe":    "setup",
	"opsdeck-0.2.0-windows-x64-portable.exe": "portable",
	"opsdeck-0.2.0-macos-universal.dmg":      "dmg",
}

func files() map[string]string {
	m := map[string]string{}
	for k, v := range release {
		m[k] = v
	}
	return m
}

func TestCLIReplacesBinary(t *testing.T) {
	ts := fakeGitHub(t, "v0.2.0", files(), "")
	exe := filepath.Join(t.TempDir(), "opsdeck")
	os.WriteFile(exe, []byte("old"), 0o755)
	u, _, quits := newTest(ts, "0.1.0", KindCLI, "linux", exe)

	st := u.Status(context.Background(), true, false)
	if !st.Available || st.Latest.Version != "0.2.0" || st.Method != "binary" || st.Latest.Notes != "notes" {
		t.Fatalf("status: %+v", st)
	}
	if err := u.Install(); err != nil {
		t.Fatal(err)
	}
	st = wait(t, u)
	if st.State != "done" || !strings.Contains(st.Message, "Restart") {
		t.Fatalf("after install: %+v", st)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new cli" {
		t.Fatalf("binary = %q", b)
	}
	if fi, _ := os.Stat(exe); fi.Mode()&0o111 == 0 {
		t.Fatal("not executable")
	}
	if quits.Load() != 0 {
		t.Fatal("the CLI was quit")
	}
}

func TestChecksumMismatchKeepsBinary(t *testing.T) {
	ts := fakeGitHub(t, "v0.2.0", files(), "opsdeck-cli-0.2.0-linux-amd64")
	exe := filepath.Join(t.TempDir(), "opsdeck")
	os.WriteFile(exe, []byte("old"), 0o755)
	u, _, _ := newTest(ts, "0.1.0", KindCLI, "linux", exe)
	u.Status(context.Background(), true, false)
	if err := u.Install(); err != nil {
		t.Fatal(err)
	}
	st := wait(t, u)
	if st.State != "failed" || !strings.Contains(st.Message, "checksum mismatch") {
		t.Fatalf("status: %+v", st)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("binary replaced: %q", b)
	}
}

func TestDesktopPlans(t *testing.T) {
	ts := fakeGitHub(t, "v0.2.0", files(), "")
	dir := t.TempDir()

	mac := filepath.Join(dir, "Applications", "opsdeck.app", "Contents", "MacOS", "opsdeck")
	os.MkdirAll(filepath.Dir(mac), 0o755)
	winInstalled := filepath.Join(dir, "Program Files", "opsdeck", "opsdeck.exe")
	os.MkdirAll(filepath.Dir(winInstalled), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(winInstalled), "uninstall.exe"), nil, 0o644)
	winPortable := filepath.Join(dir, "Downloads", "opsdeck.exe")
	os.MkdirAll(filepath.Dir(winPortable), 0o755)
	os.WriteFile(winPortable, []byte("old"), 0o755)

	for _, c := range []struct {
		name, goos, exe, owner, method, installed string
		relaunch                                  [4]string
	}{
		{"deb", "linux", "/usr/bin/opsdeck-desktop", "dpkg-query", "deb", "deb opsdeck_0.2.0_amd64.deb", [4]string{"linux", "deb", ".", "/usr/bin/opsdeck-desktop"}},
		{"rpm", "linux", "/usr/bin/opsdeck-desktop", "rpm", "rpm", "rpm opsdeck-0.2.0-1.x86_64.rpm", [4]string{"linux", "rpm", ".", "/usr/bin/opsdeck-desktop"}},
		{"mac", "darwin", mac, "", "app", "", [4]string{"darwin", "app", "opsdeck-0.2.0-macos-universal.dmg", filepath.Join(dir, "Applications", "opsdeck.app")}},
		{"windows installer", "windows", winInstalled, "", "installer", "", [4]string{"windows", "installer", "opsdeck-0.2.0-windows-x64-setup.exe", winInstalled}},
		{"windows portable", "windows", winPortable, "", "portable", "", [4]string{"windows", "portable", ".", winPortable}},
	} {
		t.Run(c.name, func(t *testing.T) {
			u, r, quits := newTest(ts, "0.1.0", KindDesktop, c.goos, c.exe)
			r.owner = c.owner
			st := u.Status(context.Background(), true, false)
			if st.Method != c.method {
				t.Fatalf("method = %q (%s)", st.Method, st.Manual)
			}
			if err := u.Install(); err != nil {
				t.Fatal(err)
			}
			st = wait(t, u)
			if st.State != "restarting" {
				t.Fatalf("status: %+v", st)
			}
			if c.installed != "" && (len(r.installed) != 1 || r.installed[0] != c.installed) {
				t.Fatalf("installed %q", r.installed)
			}
			if len(r.relaunch) != 1 || r.relaunch[0] != c.relaunch {
				t.Fatalf("relaunch %q, want %q", r.relaunch, c.relaunch)
			}
			if quits.Load() != 1 {
				t.Fatalf("quit %d times", quits.Load())
			}
		})
	}
	if b, _ := os.ReadFile(winPortable); string(b) != "portable" {
		t.Fatalf("portable exe = %q", b)
	}
}

func TestManual(t *testing.T) {
	ts := fakeGitHub(t, "v0.2.0", files(), "")
	// an unpackaged Linux desktop binary
	u, _, _ := newTest(ts, "0.1.0", KindDesktop, "linux", "/opt/opsdeck/opsdeck")
	st := u.Status(context.Background(), true, false)
	if !st.Available || st.Method != "" || !strings.Contains(st.Manual, "package") {
		t.Fatalf("status: %+v", st)
	}
	if err := u.Install(); err == nil {
		t.Fatal("installed without a method")
	}
	// a development build is not offered updates
	u, _, _ = newTest(ts, "9995b8f-dirty", KindCLI, "linux", "/tmp/opsdeck")
	st = u.Status(context.Background(), true, false)
	if st.Available || !strings.Contains(st.Manual, "development build") {
		t.Fatalf("dev status: %+v", st)
	}
	// up to date
	u, _, _ = newTest(ts, "0.2.0", KindCLI, "linux", "/tmp/opsdeck")
	if st := u.Status(context.Background(), true, false); st.Available || st.Error != "" {
		t.Fatalf("current status: %+v", st)
	}
}
