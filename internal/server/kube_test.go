package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/sshd"
)

// TestKubeLive drives the Kubernetes API against a real cluster: the
// kubeconfig in OPSDECK_TEST_KUBECONFIG (or k3s's on this machine). The
// cluster needs the busybox:1.37 image (or registry access). Skipped when
// there is none.
func TestKubeLive(t *testing.T) {
	path := os.Getenv("OPSDECK_TEST_KUBECONFIG")
	if path == "" {
		path = "/etc/rancher/k3s/k3s.yaml"
	}
	kc, err := os.ReadFile(path)
	if err != nil {
		t.Skip("no kubeconfig for a test cluster")
	}
	home := t.TempDir()
	t.Setenv("OPSDECK_HOME", home)
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 3 * time.Minute}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme"}, nil)
	c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "kubeconfig", "name": "k3s",
		"fields": []map[string]any{{"name": "kubeconfig", "value": string(kc)}}}, nil)
	ns := "opsdeck-test-" + strconv.FormatInt(time.Now().UnixNano()%1e6, 10)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "KubernetesCluster", "name": "k3s",
		"spec": map[string]any{"kubeconfig": "vault://acme/k3s", "namespace": ns}}, nil)
	K := "/api/workspaces/acme/clusters/k3s"
	var overview map[string]any
	if code := c.do("GET", K+"/overview", nil, &overview); code != 200 {
		t.Skipf("test cluster not reachable: %v", overview)
	}
	defer c.do("DELETE", K+"/object/namespaces/_/"+ns, nil, nil)

	manifest := `apiVersion: v1
kind: Namespace
metadata: {name: ` + ns + `}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
spec:
  replicas: 2
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      terminationGracePeriodSeconds: 1
      containers:
      - name: web
        image: busybox:1.37
        imagePullPolicy: IfNotPresent
        command: ["sh", "-c", "mkdir -p /www && echo hello-k8s > /www/index.html && httpd -p 8080 -h /www && i=0; while true; do i=$((i+1)); echo tick $i; sleep 1; done"]
        ports: [{name: http, containerPort: 8080}]
---
apiVersion: v1
kind: Service
metadata: {name: web}
spec:
  selector: {app: web}
  ports: [{port: 80, targetPort: http}]
---
apiVersion: v1
kind: Secret
metadata: {name: db}
stringData: {password: S3cr3t-K8s-Pw}
---
apiVersion: batch/v1
kind: CronJob
metadata: {name: report}
spec:
  schedule: "0 3 * * *"
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: Never
          containers: [{name: r, image: busybox:1.37, imagePullPolicy: IfNotPresent, command: ["echo", "report done"]}]
`
	// a dry run changes nothing and shows what would be created
	var res []map[string]any
	c.must("POST", K+"/apply", map[string]any{"manifest": manifest, "namespace": ns, "dryRun": true}, &res)
	if len(res) != 5 || res[0]["action"] != "created" || res[1]["action"] != "created" || !strings.Contains(res[1]["diff"].(string), "+  replicas: 2") {
		t.Fatalf("dry run: %v", res)
	}
	if strings.Contains(res[3]["diff"].(string), "S3cr3t") {
		t.Fatal("secret value in the dry-run diff")
	}
	if code := c.do("GET", K+"/object/deployments/"+ns+"/web", nil, nil); code != 404 {
		t.Fatalf("dry run created the deployment (%d)", code)
	}
	c.must("POST", K+"/apply", map[string]any{"manifest": manifest, "namespace": ns}, &res)
	for _, r := range res {
		if r["action"] != "created" {
			t.Fatalf("apply: %v", res)
		}
	}
	c.must("POST", K+"/apply", map[string]any{"manifest": manifest, "namespace": ns, "dryRun": true}, &res)
	if res[1]["action"] != "unchanged" {
		t.Fatalf("re-apply: %v", res[1])
	}

	// wait for the deployment
	var tbl struct {
		Columns []map[string]any
		Rows    []map[string]any
	}
	waitPods := func(n int) {
		t.Helper()
		for i := 0; i < 120; i++ {
			c.must("GET", K+"/list/pods?namespace="+ns+"&labelSelector=app%3Dweb", nil, &tbl)
			running, live := 0, 0
			for _, r := range tbl.Rows {
				if r["deleting"] == nil {
					live++
					if r["cells"].([]any)[2] == "Running" {
						running++
					}
				}
			}
			if running == n && live == n {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("pods did not reach %d running: %v", n, tbl.Rows)
	}
	waitPods(2)
	c.must("GET", K+"/list/deployments?namespace="+ns, nil, &tbl)
	if tbl.Columns[1]["name"] != "Ready" || tbl.Rows[0]["name"] != "web" {
		t.Fatalf("deployments table: %v %v", tbl.Columns, tbl.Rows)
	}
	var types []map[string]any
	c.must("GET", K+"/resources", nil, &types)
	found := false
	for _, ty := range types {
		found = found || (ty["id"] == "deployments.apps" && ty["category"] == "Workloads")
	}
	if !found {
		t.Fatal("deployments.apps not discovered")
	}
	c.must("GET", K+"/overview?namespace="+ns, nil, &overview)
	if len(overview["nodes"].([]any)) == 0 || overview["pods"].(map[string]any)["running"].(float64) < 2 {
		t.Fatalf("overview: %v", overview)
	}

	// secrets are masked until revealed
	var obj map[string]any
	c.must("GET", K+"/object/secrets/"+ns+"/db", nil, &obj)
	if strings.Contains(obj["yaml"].(string), "S3cr3t") || strings.Contains(obj["yaml"].(string), "UzNjcjN0") {
		t.Fatalf("secret yaml shows the value: %s", obj["yaml"])
	}
	var revealed map[string]string
	c.must("POST", K+"/object/secrets/"+ns+"/db/reveal", nil, &revealed)
	if revealed["password"] != "S3cr3t-K8s-Pw" {
		t.Fatalf("reveal: %v", revealed)
	}

	// logs of every pod of the deployment
	time.Sleep(2 * time.Second)
	lines := sse(t, c, K+"/logs/stream?type=deployments&namespace="+ns+"&name=web&lines=5")
	srcs := map[string]bool{}
	for _, l := range lines {
		if strings.HasPrefix(l["line"].(string), "tick ") && l["ts"] != nil {
			srcs[l["src"].(string)] = true
		}
	}
	if len(srcs) != 2 {
		t.Fatalf("deployment logs from %v: %v", srcs, lines)
	}

	// exec into a pod
	pod := tbl.Rows[0]
	c.must("GET", K+"/object/deployments/"+ns+"/web/pods", nil, &tbl)
	podName := tbl.Rows[0]["name"].(string)
	_ = pod
	u, _ := http.NewRequest("GET", ts.URL, nil)
	hdr := http.Header{}
	for _, ck := range jar.Cookies(u.URL) {
		hdr.Add("Cookie", ck.Name+"="+ck.Value)
	}
	wsc, _, err := websocket.DefaultDialer.Dial(strings.Replace(ts.URL, "http", "ws", 1)+K+"/exec/"+ns+"/"+podName+"?cols=80&rows=24", hdr)
	if err != nil {
		t.Fatal(err)
	}
	wsc.WriteJSON(map[string]any{"t": "d", "d": "echo in-$((40+2)) && cat /www/index.html\n"})
	var term strings.Builder
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(term.String(), "hello-k8s") && time.Now().Before(deadline) {
		wsc.SetReadDeadline(deadline)
		var m map[string]any
		if err := wsc.ReadJSON(&m); err != nil {
			t.Fatalf("exec read: %v (%q)", err, term.String())
		}
		if d, ok := m["d"].(string); ok {
			term.WriteString(d)
		}
	}
	if !strings.Contains(term.String(), "in-42") {
		t.Fatalf("exec output %q", term.String())
	}
	wsc.Close()

	// port-forward to the service (named target port)
	var fw map[string]any
	c.must("POST", K+"/forward", map[string]any{"type": "services", "namespace": ns, "name": "web", "port": 80}, &fw)
	resp, err := http.Get(fw["url"].(string) + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.TrimSpace(string(body)) != "hello-k8s" {
		t.Fatalf("port-forward got %q", body)
	}
	c.must("DELETE", "/api/forwards/"+fw["id"].(string), nil, nil)

	// scale, image, history, undo
	c.must("POST", K+"/object/deployments/"+ns+"/web/scale", map[string]any{"replicas": 3}, nil)
	waitPods(3)
	c.must("POST", K+"/object/deployments/"+ns+"/web/image", map[string]any{"image": "busybox:1.37-alt"}, nil)
	var hist []map[string]any
	for i := 0; i < 30; i++ {
		c.must("GET", K+"/object/deployments/"+ns+"/web/history", nil, &hist)
		if len(hist) == 2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(hist) != 2 || hist[0]["images"].([]any)[0] != "busybox:1.37-alt" || hist[0]["current"] != true {
		t.Fatalf("history: %v", hist)
	}
	c.must("POST", K+"/object/deployments/"+ns+"/web/undo", map[string]any{}, nil)
	c.must("GET", K+"/object/deployments/"+ns+"/web", nil, &obj)
	if !strings.Contains(obj["yaml"].(string), "image: busybox:1.37\n") {
		t.Fatalf("undo did not restore the image:\n%s", obj["yaml"])
	}
	waitPods(3)

	// events: the recent ones, as a live stream
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+K+"/events/stream?namespace="+ns, nil)
	er, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	sawScale := false
	sc := bufio.NewScanner(er.Body)
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var m struct{ Event map[string]any }
			json.Unmarshal([]byte(d), &m)
			if m.Event["reason"] == "ScalingReplicaSet" {
				sawScale = true
				break
			}
		}
	}
	er.Body.Close()
	if !sawScale {
		t.Fatal("no ScalingReplicaSet event")
	}

	// cronjob: run it now
	var out map[string]string
	c.must("POST", K+"/object/cronjobs/"+ns+"/report/trigger", nil, &out)
	job := strings.TrimPrefix(out["output"], "started job ")
	c.must("GET", K+"/object/jobs/"+ns+"/"+job, nil, &obj)

	// the same cluster through a jump host (exec and port-forward use the proxy too)
	var key map[string]any
	c.must("POST", "/api/vault/ssh/generate", map[string]any{"scope": "acme", "name": "bk", "algorithm": "ed25519"}, &key)
	pub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(key["publicKey"].(string)))
	bastion, err := sshd.Start("127.0.0.1:0", sshd.Options{AuthorizedKeys: []ssh.PublicKey{pub}, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer bastion.Close()
	_, bp, _ := net.SplitHostPort(bastion.Addr)
	bport, _ := strconv.Atoi(bp)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "bastion",
		"spec": map[string]any{"address": "127.0.0.1", "port": bport, "auth": "vault://acme/bk"}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "KubernetesCluster", "name": "viak3s", "via": "host/bastion",
		"spec": map[string]any{"kubeconfig": "vault://acme/k3s", "namespace": ns}}, nil)
	V := "/api/workspaces/acme/clusters/viak3s"
	c.must("GET", V+"/list/pods?namespace="+ns, nil, &tbl)
	if len(tbl.Rows) < 3 {
		t.Fatalf("via: pods %v", tbl.Rows)
	}
	c.must("POST", V+"/forward", map[string]any{"type": "deployments", "namespace": ns, "name": "web", "port": 8080}, &fw)
	resp, err = http.Get(fw["url"].(string) + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.TrimSpace(string(body)) != "hello-k8s" {
		t.Fatalf("port-forward via bastion got %q", body)
	}

	// delete
	c.must("DELETE", K+"/object/deployments/"+ns+"/web", nil, nil)
	if code := c.do("GET", K+"/object/deployments/"+ns+"/web", nil, nil); code != 404 {
		t.Fatalf("deleted deployment still there (%d)", code)
	}
}
