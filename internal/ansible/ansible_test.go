package ansible

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/workspace"
)

// Made with ansible-vault (ansible-core 2.21), password "hunter2".
const fixture11 = `$ANSIBLE_VAULT;1.1;AES256
36623961343365616538626464306330303837363637313263303532323662633937383130356562
6266613539346466666666373837356361336262393064660a376333623538363762643530363931
32396261666437626361666164653239383135623533653333356439653765386533346463343664
6431366564383931610a383334393433613830663161656531353061323438306265653332333862
39666536663461396331313132626238333132346237323034643962663064303737643533653634
6331346334396365623262616164303063643535636333623139
`

const fixture12 = `$ANSIBLE_VAULT;1.2;AES256;prod
35656133363731663339313039393239663932313735313132626433333635303232353465623737
3866353461383033376332303238636437376561623862350a373664303637366638346664616435
39333866646135326466323737306139643965323835306239363031386530633339336537636330
6566613562383832350a313231313564383233643633633166326230633866396264343432356363
3838
`

const inlineFixture = `user: deploy
db_pass: !vault |
          $ANSIBLE_VAULT;1.1;AES256
          37396334666466623437323664323330633061653862633630356130353139613361353565323131
          6230303761363532326437303932353266373934333730310a646330353939316134366434396539
          62663664333663356464363839613131383138383964633238386138326163656336383933323932
          6437323238383461620a383664633163393330373762373231306561386263313933626230333365
          6534
port: 5432
`

func TestDecryptAnsibleFixtures(t *testing.T) {
	b, err := Decrypt(fixture11, "hunter2")
	if err != nil || string(b) != "db_password: s3cret\napi_key: abc123\n" {
		t.Fatalf("1.1: %q %v", b, err)
	}
	h, _ := ParseHeader(fixture12)
	if h.ID != "prod" || h.Version != "1.2" {
		t.Fatalf("header %+v", h)
	}
	b, err = Decrypt(fixture12, "hunter2")
	if err != nil || string(b) != "line1\nline2 é\n" {
		t.Fatalf("1.2: %q %v", b, err)
	}
	if _, err := Decrypt(fixture11, "wrong"); err != ErrWrongPassword {
		t.Fatalf("wrong password: %v", err)
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	for _, plain := range []string{"", "x", "exactly sixteen!", strings.Repeat("ab\n", 500)} {
		text, err := Encrypt([]byte(plain), "pw", "staging")
		if err != nil {
			t.Fatal(err)
		}
		if !IsEncrypted([]byte(text)) || !strings.HasPrefix(text, "$ANSIBLE_VAULT;1.2;AES256;staging\n") {
			t.Fatalf("header: %q", text[:40])
		}
		for _, l := range strings.Split(strings.TrimSpace(text), "\n")[1:] {
			if len(l) > 80 {
				t.Fatalf("line longer than 80: %d", len(l))
			}
		}
		b, err := Decrypt(text, "pw")
		if err != nil || string(b) != plain {
			t.Fatalf("round trip %q: %q %v", plain, b, err)
		}
	}
	if _, err := Encrypt([]byte("x"), "", ""); err == nil {
		t.Fatal("empty password accepted")
	}
}

// ansible-vault itself must open what we write.
func TestAnsibleVaultOpensOurs(t *testing.T) {
	bin, err := exec.LookPath("ansible-vault")
	if err != nil {
		t.Skip("ansible-vault not installed")
	}
	dir := t.TempDir()
	text, _ := Encrypt([]byte("hello: world\n"), "s3cr3t", "")
	os.WriteFile(filepath.Join(dir, "v.yml"), []byte(text), 0o600)
	os.WriteFile(filepath.Join(dir, "pw"), []byte("s3cr3t\n"), 0o600)
	out, err := exec.Command(bin, "view", "--vault-password-file", filepath.Join(dir, "pw"), filepath.Join(dir, "v.yml")).CombinedOutput()
	if err != nil || string(out) != "hello: world\n" {
		t.Fatalf("ansible-vault view: %s %v", out, err)
	}
}

func TestInline(t *testing.T) {
	in := FindInline(inlineFixture)
	if len(in) != 1 || in[0].Key != "db_pass" || in[0].Line != 2 || in[0].End != 8 {
		t.Fatalf("%+v", in)
	}
	v, err := in[0].DecryptValue("hunter2")
	if err != nil || v != "topsecret" {
		t.Fatalf("%q %v", v, err)
	}
	out, locked := RevealInline(inlineFixture, func(i Inline) (string, error) { return i.DecryptValue("hunter2") })
	if locked != 0 || !strings.Contains(out, `db_pass: "topsecret"  # !vault`) || !strings.Contains(out, "port: 5432") || strings.Contains(out, "ANSIBLE_VAULT") {
		t.Fatalf("reveal:\n%s", out)
	}
	block, err := EncryptString("token", "abc", "pw", "", 2)
	if err != nil || !strings.HasPrefix(block, "  token: !vault |\n            $ANSIBLE_VAULT;1.1;AES256\n") {
		t.Fatalf("%q %v", block, err)
	}
	got := FindInline("x:\n" + block)
	if len(got) != 1 || got[0].Key != "token" {
		t.Fatalf("%+v", got)
	}
	if v, _ := got[0].DecryptValue("pw"); v != "abc" {
		t.Fatalf("value %q", v)
	}
}

func TestSetYAMLKey(t *testing.T) {
	src := "---\na: 1\nb: |\n  line\n\n  more\nc: 3\n"
	out, err := SetYAMLKey(src, "b", `"x"`)
	if err != nil || out != "---\na: 1\nb: \"x\"\nc: 3\n" {
		t.Fatalf("%q %v", out, err)
	}
	out, _ = SetYAMLKey(src, "d", YAMLString("new"))
	if !strings.HasSuffix(out, "c: 3\nd: \"new\"\n") {
		t.Fatalf("%q", out)
	}
	out, _ = SetYAMLKey(inlineFixture, "db_pass", `"plain"`)
	if out != "user: deploy\ndb_pass: \"plain\"\nport: 5432\n" {
		t.Fatalf("%q", out)
	}
	if _, err := SetYAMLKey("- a\n- b\n", "x", "1"); err == nil {
		t.Fatal("list accepted")
	}
	if _, err := SetYAMLKey("a: 1\n", "bad key", "1"); err == nil {
		t.Fatal("bad key accepted")
	}
}

func testProject(t *testing.T) *Project {
	t.Helper()
	dir := t.TempDir()
	w := &workspace.Workspace{Path: dir}
	w.Metadata.Name = "acme"
	w.Resources = []*workspace.Resource{{Kind: Kind, Name: "infra", Spec: map[string]any{"source": "folder"}}}
	p, err := Open(w, "infra")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectFiles(t *testing.T) {
	p := testProject(t)
	if p.Root != filepath.Join(p.W.Path, "ansible", "infra") || p.External() {
		t.Fatalf("root %s external %v", p.Root, p.External())
	}
	files := map[string]string{
		"site.yml":                   "- hosts: all\n  tasks: []\n",
		"playbooks/db.yaml":          "---\n- name: db\n  hosts: db\n  roles: [pg]\n",
		"notplay.yml":                "a: 1\n",
		"inventories/prod/hosts.yml": "all: {}\n",
		"inventories/prod/group_vars/all/vault.yml": fixture12,
		"group_vars/all.yml":                        inlineFixture,
		"roles/pg/tasks/main.yml":                   "- name: x\n  debug: msg=hi\n",
		"roles/pg/templates/pg.conf.j2":             "x",
		"ansible.cfg":                               "[defaults]\ninventory = ./inventories/prod\n",
		"requirements.yml":                          "collections: []\n",
	}
	for rel, c := range files {
		if err := p.Write(rel, c); err != nil {
			t.Fatal(rel, err)
		}
	}
	list, _, err := p.Files()
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]File{}
	for _, f := range list {
		types[f.Path] = f
	}
	want := map[string]string{"site.yml": TypePlaybook, "playbooks/db.yaml": TypePlaybook, "notplay.yml": TypeFile, "inventories/prod/hosts.yml": TypeInventory,
		"group_vars/all.yml": TypeVars, "roles/pg/tasks/main.yml": TypeRole, "roles/pg/templates/pg.conf.j2": TypeTemplate, "ansible.cfg": TypeConfig, "requirements.yml": TypeRequirements}
	for path, typ := range want {
		if types[path].Type != typ {
			t.Errorf("%s: %s, want %s", path, types[path].Type, typ)
		}
	}
	if f := types["inventories/prod/group_vars/all/vault.yml"]; !f.Encrypted || f.VaultID != "prod" {
		t.Errorf("vault file %+v", f)
	}
	if types["group_vars/all.yml"].Inline != 1 {
		t.Errorf("inline %+v", types["group_vars/all.yml"])
	}
	s := p.Summarize()
	if len(s.Playbooks) != 2 || s.VaultFiles != 1 || s.InlineVars != 1 || strings.Join(s.VaultIDs, ",") != "default,prod" || !s.Config {
		t.Errorf("summary %+v", s)
	}
	if strings.Join(s.Inventories, ",") != "inventories/prod" {
		t.Errorf("inventories %v", s.Inventories)
	}
	for _, bad := range []string{"../x", "/etc/passwd", ".git/config", "a/../../x"} {
		if _, err := p.Path(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	// a symlink out of the project is refused
	os.Symlink("/etc", filepath.Join(p.Root, "out"))
	if _, err := p.Read("out/hostname"); err == nil {
		t.Error("symlink escape accepted")
	}
	hits, _ := p.Search("debug", false, 10)
	if len(hits) != 1 || hits[0].Path != "roles/pg/tasks/main.yml" {
		t.Errorf("search %+v", hits)
	}
}

func TestVaultOperations(t *testing.T) {
	p := testProject(t)
	m := New(nil)
	ctx := t.Context()
	p.Write("vault.yml", fixture12)
	p.Write("vars.yml", inlineFixture)
	if _, err := m.View(ctx, p, "vault.yml"); err == nil {
		t.Fatal("opened without a password")
	} else if np, ok := IsNeedPassword(err); !ok || np.VaultID != "prod" {
		t.Fatalf("%v", err)
	}
	if ok, err := CheckPassword(p, "prod", "nope"); !ok || err == nil {
		t.Fatal("wrong password accepted")
	}
	if ok, err := CheckPassword(p, "prod", "hunter2"); !ok || err != nil {
		t.Fatal(err)
	}
	m.Remember(p, "prod", "hunter2")
	m.Remember(p, "default", "hunter2")
	plain, err := m.View(ctx, p, "vault.yml")
	if err != nil || plain != "line1\nline2 é\n" {
		t.Fatalf("%q %v", plain, err)
	}
	if err := m.SaveEncrypted(ctx, p, "vault.yml", "a: 1\n", ""); err != nil {
		t.Fatal(err)
	}
	fc, _ := p.Read("vault.yml")
	if !fc.Encrypted || fc.VaultID != "prod" {
		t.Fatalf("kept vault id: %+v", fc)
	}
	if _, err := m.SetVar(ctx, p, "vault.yml", "b", "two", ""); err != nil {
		t.Fatal(err)
	}
	if plain, _ := m.View(ctx, p, "vault.yml"); plain != "a: 1\nb: \"two\"\n" {
		t.Fatalf("%q", plain)
	}
	if _, err := m.SetVar(ctx, p, "vars.yml", "api_token", "xyz", ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := m.View(ctx, p, "vars.yml"); !strings.Contains(v, `api_token: "xyz"`) || !strings.Contains(v, `db_pass: "topsecret"`) {
		t.Fatalf("%s", v)
	}
	if err := m.EncryptFile(ctx, p, "vars.yml", "default"); err != nil {
		t.Fatal(err)
	}
	if err := m.DecryptFile(ctx, p, "vars.yml"); err != nil {
		t.Fatal(err)
	}
	if err := m.Rekey(ctx, p, "vault.yml", "new-pw", "prod"); err != nil {
		t.Fatal(err)
	}
	m.Forget(p, "")
	if len(m.Remembered(p)) != 0 {
		t.Fatal("not forgotten")
	}
	m.Remember(p, "prod", "new-pw")
	if plain, _ := m.View(ctx, p, "vault.yml"); plain != "a: 1\nb: \"two\"\n" {
		t.Fatalf("after rekey: %q", plain)
	}
}

func needAnsible(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook not installed")
	}
}

func waitRun(t *testing.T, r *Run) RunInfo {
	t.Helper()
	select {
	case <-r.Done():
	case <-time.After(3 * time.Minute):
		t.Fatal("run did not finish")
	}
	return r.Info()
}

func TestRunPlaybook(t *testing.T) {
	needAnsible(t)
	p := testProject(t)
	m := New(nil)
	ctx := t.Context()
	vaultText, _ := Encrypt([]byte("secret_word: swordfish\n"), "pw1", "")
	files := map[string]string{
		"inventory.ini": "localhost ansible_connection=local ansible_python_interpreter=" + pythonFor(t) + "\n",
		"vault.yml":     vaultText,
		"site.yml": `- hosts: all
  gather_facts: false
  vars_files: [vault.yml]
  tasks:
    - name: Say the word
      ansible.builtin.debug:
        msg: "the word has {{ secret_word | length }} letters, extra={{ extra | default('none') }}"
    - name: Touch a file
      ansible.builtin.copy:
        dest: "{{ playbook_dir }}/out.txt"
        content: "hello\n"
`,
	}
	for k, v := range files {
		p.Write(k, v)
	}
	o := RunOptions{Playbook: "site.yml", Inventory: "inventory.ini", ExtraVars: "extra: 42"}
	if _, err := m.Start(ctx, p, o, "user"); err == nil {
		t.Fatal("started without a vault password")
	} else if _, ok := IsNeedPassword(err); !ok {
		t.Fatal(err)
	}
	r, err := m.Start(WithPassword(ctx, p, "default", "pw1"), p, o, "user")
	if err != nil {
		t.Fatal(err)
	}
	var streamed []string
	followed := make(chan error, 1)
	go func() {
		followed <- r.Follow(ctx, 0, func(l LogLine) error { streamed = append(streamed, StripANSI(l.Text)); return nil })
	}()
	info := waitRun(t, r)
	if err := <-followed; err != nil {
		t.Fatal(err)
	}
	log := strings.Join(streamed, "\n")
	if info.Status != StatusOK || info.ExitCode != 0 {
		t.Fatalf("status %s (%d)\n%s", info.Status, info.ExitCode, log)
	}
	if !strings.Contains(log, "the word has 9 letters, extra=42") {
		t.Fatalf("log:\n%s", log)
	}
	if len(info.Recap) != 1 || info.Recap[0].Host != "localhost" || info.Recap[0].Changed != 1 || info.Recap[0].OK != 2 {
		t.Fatalf("recap %+v", info.Recap)
	}
	if strings.Contains(info.Command, "/tmp") || !strings.Contains(info.Command, "<password>") {
		t.Fatalf("command shows private files: %s", info.Command)
	}
	if strings.Contains(log, "swordfish") {
		t.Fatal("secret in log")
	}
	// history and the saved log
	runs := m.Runs(p.W, "infra", 10)
	if len(runs) != 1 || runs[0].Status != StatusOK {
		t.Fatalf("history %+v", runs)
	}
	m2 := New(nil) // a restarted opsdeck reads it back
	old, err := m2.Get(p.W, info.ID)
	if err != nil || old.Info().Status != StatusOK || !strings.Contains(strings.Join(old.Tail(0), "\n"), "9 letters") {
		t.Fatalf("reloaded run: %v %+v", err, old)
	}
	if !strings.Contains(r.Summary(20), "localhost: ok=2 changed=1") {
		t.Fatalf("summary:\n%s", r.Summary(20))
	}

	// check mode changes nothing; a failing task is reported
	os.Remove(filepath.Join(p.Root, "out.txt"))
	r, _ = m.Start(WithPassword(ctx, p, "default", "pw1"), p, RunOptions{Playbook: "site.yml", Inventory: "inventory.ini", Check: true, Diff: true}, "user")
	if info := waitRun(t, r); info.Status != StatusOK {
		t.Fatalf("check run %s", info.Status)
	}
	if _, err := os.Stat(filepath.Join(p.Root, "out.txt")); err == nil {
		t.Fatal("check mode wrote the file")
	}
	p.Write("fail.yml", "- hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.fail: { msg: broken on purpose }\n")
	r, _ = m.Start(WithPassword(ctx, p, "default", "pw1"), p, RunOptions{Playbook: "fail.yml", Inventory: "inventory.ini"}, "user")
	if info := waitRun(t, r); info.Status != StatusFailed || info.Recap[0].Failed != 1 || !strings.Contains(r.Summary(0), "broken on purpose") {
		t.Fatalf("failed run %+v\n%s", info, r.Summary(0))
	}

	// cancel stops a long run
	p.Write("slow.yml", "- hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.pause: { seconds: 120 }\n")
	r, _ = m.Start(WithPassword(ctx, p, "default", "pw1"), p, RunOptions{Playbook: "slow.yml", Inventory: "inventory.ini"}, "user")
	time.Sleep(3 * time.Second)
	r.Cancel()
	if info := waitRun(t, r); info.Status != StatusCanceled {
		t.Fatalf("canceled run %s", info.Status)
	}

	// ad-hoc and quick commands
	m.Remember(p, "default", "pw1")
	r, err = m.Start(ctx, p, RunOptions{Kind: KindAdhoc, Inventory: "inventory.ini", Module: "ansible.builtin.command", Args: "echo adhoc-ok"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if info := waitRun(t, r); info.Status != StatusOK || !strings.Contains(strings.Join(r.Tail(0), "\n"), "adhoc-ok") {
		t.Fatalf("adhoc %+v %v", info, r.Tail(0))
	}
	out, err := m.Exec(ctx, p, RunOptions{Inventory: "inventory.ini"}, true, "ansible-playbook", "--list-tasks", "--", "site.yml")
	if err != nil || !strings.Contains(out, "Say the word") {
		t.Fatalf("list-tasks: %v\n%s", err, out)
	}
	if _, err := m.Exec(ctx, p, RunOptions{Inventory: "inventory.ini"}, true, "ansible-playbook", "--syntax-check", "--", "fail.yml"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []RunOptions{{Playbook: "../x.yml"}, {Playbook: "site.yml", Limit: "web\n--become"}, {Kind: KindAdhoc, Module: "x; rm"}, {Playbook: "site.yml", ExtraVars: "- list"}} {
		if err := bad.Validate(p); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func pythonFor(t *testing.T) string {
	// the interpreter that runs ansible itself
	path, _ := exec.LookPath("ansible-playbook")
	b, _ := os.ReadFile(path)
	first, _, _ := strings.Cut(string(b), "\n")
	if strings.HasPrefix(first, "#!") {
		return strings.TrimSpace(strings.TrimPrefix(first, "#!"))
	}
	return "python3"
}
