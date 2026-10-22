package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"opsdeck/internal/connect"
	"opsdeck/internal/kube"
	"opsdeck/internal/workspace"
)

func (s *Server) kubeRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	C := "/api/workspaces/{ws}/clusters/{c}"
	O := "/object/{type}/{ns}/{name}"
	get := func(p string, fn kubeFn) { mux.HandleFunc("GET "+C+p, u(s.kubeRead(fn))) }
	do := func(method, p, action string, fn kubeFn) { mux.HandleFunc(method+" "+C+p, u(s.kubeChange(action, fn))) }

	get("/overview", func(c *kctx) (any, error) { return c.conn.Overview(c.ctx, c.nsAll()) })
	get("/resources", func(c *kctx) (any, error) { return c.conn.Resources(c.ctx, c.q("refresh") == "1") })
	get("/namespaces", func(c *kctx) (any, error) {
		l, err := c.conn.CS.CoreV1().Namespaces().List(c.ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		out := []string{}
		for _, n := range l.Items {
			out = append(out, n.Name)
		}
		sort.Strings(out)
		return map[string]any{"namespaces": out, "default": c.conn.NS}, nil
	})
	get("/list/{type}", func(c *kctx) (any, error) {
		rt, err := c.rt()
		if err != nil {
			return nil, err
		}
		return c.conn.List(c.ctx, rt, kube.TableOptions{Namespace: c.nsAll(), LabelSelector: c.q("labelSelector"), FieldSelector: c.q("fieldSelector")})
	})
	get(O, func(c *kctx) (any, error) {
		rt, err := c.rt()
		if err != nil {
			return nil, err
		}
		o, err := c.conn.Get(c.ctx, rt, c.ns(), c.name())
		if err != nil {
			return nil, err
		}
		y, err := kube.YAMLOf(kube.Display(o), false)
		if err != nil {
			return nil, err
		}
		ey, err := kube.YAMLOf(kube.Clean(o), false)
		return map[string]any{"object": kube.MaskSecret(o), "yaml": y, "editYaml": ey, "type": rt}, err
	})
	get(O+"/events", func(c *kctx) (any, error) {
		rt, err := c.rt()
		if err != nil {
			return nil, err
		}
		o, err := c.conn.Get(c.ctx, rt, c.ns(), c.name())
		if err != nil {
			return nil, err
		}
		md, _ := o["metadata"].(map[string]any)
		uid, _ := md["uid"].(string)
		return c.conn.Events(c.ctx, c.ns(), uid)
	})
	get(O+"/pods", func(c *kctx) (any, error) {
		rt, err := c.rt()
		if err != nil {
			return nil, err
		}
		pods, err := c.conn.PodsFor(c.ctx, rt, c.ns(), c.name())
		if err != nil {
			return nil, err
		}
		prt, err := c.conn.Resolve(c.ctx, "pods")
		if err != nil {
			return nil, err
		}
		// the pods' table, limited to these pods
		want := map[string]bool{}
		nss := map[string]bool{}
		for _, p := range pods {
			want[p.Namespace+"/"+p.Name] = true
			nss[p.Namespace] = true
		}
		ns := ""
		if len(nss) == 1 {
			for k := range nss {
				ns = k
			}
		}
		opts := kube.TableOptions{Namespace: ns}
		if rt.ID == "nodes" {
			opts.FieldSelector = "spec.nodeName=" + c.name()
		}
		t, err := c.conn.List(c.ctx, prt, opts)
		if err != nil {
			return nil, err
		}
		rows := t.Rows[:0]
		for _, r := range t.Rows {
			if want[r.Namespace+"/"+r.Name] {
				rows = append(rows, r)
			}
		}
		t.Rows = rows
		return t, nil
	})
	get(O+"/history", func(c *kctx) (any, error) {
		rt, err := c.rt()
		if err != nil {
			return nil, err
		}
		return c.conn.History(c.ctx, rt, c.ns(), c.name())
	})
	get("/metrics/pods", func(c *kctx) (any, error) { return c.conn.PodMetrics(c.ctx, c.nsAll()) })
	get("/metrics/nodes", func(c *kctx) (any, error) { return c.conn.NodeMetrics(c.ctx) })
	get("/helm", func(c *kctx) (any, error) { return c.conn.HelmReleases(c.ctx, c.nsAll()) })
	get("/helm/{ns}/{name}", func(c *kctx) (any, error) {
		rev, _ := strconv.Atoi(c.q("revision"))
		return c.conn.HelmRelease(c.ctx, c.ns(), c.name(), rev)
	})

	do("POST", "/apply", "k8s.apply", func(c *kctx) (any, error) {
		var in struct {
			Manifest, Namespace string
			DryRun              bool
		}
		if err := c.body(&in); err != nil {
			return nil, err
		}
		ns := in.Namespace
		if ns == "" || ns == "_" {
			ns = c.conn.NS
		}
		res, err := c.conn.Apply(c.ctx, in.Manifest, ns, in.DryRun)
		if err != nil {
			return nil, err
		}
		var objs []string
		for _, r := range res {
			objs = append(objs, fmt.Sprintf("%s/%s %s", r.Kind, r.Name, r.Action))
		}
		c.detail["objects"], c.detail["dryRun"] = objs, in.DryRun
		if in.DryRun {
			c.skipAudit = true
		}
		return res, nil
	})
	do("POST", O+"/reveal", "k8s.secret.reveal", func(c *kctx) (any, error) {
		if c.r.PathValue("type") != "secrets" {
			return nil, errors.New("only secrets can be revealed")
		}
		return c.conn.SecretData(c.ctx, c.ns(), c.name())
	})
	do("DELETE", O, "k8s.delete", func(c *kctx) (any, error) {
		rt, err := c.rt()
		if err != nil {
			return nil, err
		}
		return okv(c.conn.Delete(c.ctx, rt, c.ns(), c.name(), c.q("force") == "1"))
	})
	do("POST", O+"/{action}", "", func(c *kctx) (any, error) {
		rt, err := c.rt()
		if err != nil {
			return nil, err
		}
		var in struct {
			Replicas  int32
			Container string
			Image     string
			Revision  int64
		}
		if err := c.body(&in); err != nil {
			return nil, err
		}
		action := c.r.PathValue("action")
		c.action = "k8s." + action
		ns, name := c.ns(), c.name()
		switch action {
		case "scale":
			c.detail["replicas"] = in.Replicas
			return okv(c.conn.Scale(c.ctx, rt, ns, name, in.Replicas))
		case "restart":
			return okv(c.conn.Restart(c.ctx, rt, ns, name))
		case "image":
			c.detail["container"], c.detail["image"] = in.Container, in.Image
			return okv(c.conn.SetImage(c.ctx, rt, ns, name, in.Container, in.Image))
		case "pause", "resume":
			return okv(c.conn.SetPaused(c.ctx, rt, ns, name, action == "pause"))
		case "suspend", "unsuspend":
			return okv(c.conn.SetSuspended(c.ctx, rt, ns, name, action == "suspend"))
		case "trigger":
			job, err := c.conn.TriggerCronJob(c.ctx, ns, name)
			return map[string]string{"output": "started job " + job}, err
		case "undo":
			rev, err := c.conn.Undo(c.ctx, rt, ns, name, in.Revision)
			c.detail["revision"] = rev
			return map[string]string{"output": fmt.Sprintf("rolled back to revision %d", rev)}, err
		case "cordon", "uncordon":
			return okv(c.conn.Cordon(c.ctx, name, action == "cordon"))
		case "drain":
			report, err := c.conn.Drain(c.ctx, name)
			return map[string]string{"output": strings.Join(report, "\n")}, err
		}
		return nil, fmt.Errorf("unknown action %q", action)
	})
	do("POST", "/forward", "k8s.port_forward", func(c *kctx) (any, error) {
		var in struct {
			Type, Namespace, Name string
			Port                  int
		}
		if err := c.body(&in); err != nil {
			return nil, err
		}
		rt, err := c.conn.Resolve(c.ctx, in.Type)
		if err != nil {
			return nil, err
		}
		ns := in.Namespace
		if ns == "" {
			ns = c.conn.NS
		}
		pod, port, err := c.conn.ForwardTarget(c.ctx, rt, ns, in.Name, in.Port)
		if err != nil {
			return nil, err
		}
		c.tgt = c.cl.ID() + "/" + rt.Resource + "/" + ns + "/" + in.Name + ":" + strconv.Itoa(in.Port)
		conn := c.conn
		dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
			return conn.DialPod(ctx, ns, pod, port)
		}
		// check the port answers before handing out a URL
		if pc, err := dial(c.ctx, "tcp", ""); err != nil {
			return nil, fmt.Errorf("cannot reach %s:%d: %w", pod, port, err)
		} else {
			pc.Close()
		}
		fw, err := s.forwards.Start(c.ws.Name(), c.cl.ID()+"/"+rt.Resource+"/"+ns+"/"+in.Name, fmt.Sprintf("%s:%d (pod %s:%d)", in.Name, in.Port, pod, port), "", dial)
		if err != nil {
			return nil, err
		}
		fw.URL = fmt.Sprintf("http://127.0.0.1:%d", fw.LocalPort)
		return fw, nil
	})

	mux.HandleFunc("GET "+C+"/logs/stream", u(s.kubeLogStream))
	mux.HandleFunc("GET "+C+"/events/stream", u(s.kubeEventStream))
	mux.HandleFunc("GET "+C+"/exec/{ns}/{pod}", u(s.kubeExec))
}

// kctx is what a kubernetes handler works with.
type kctx struct {
	ctx       context.Context
	r         *http.Request
	ws        *workspace.Workspace
	cl        *workspace.Resource
	conn      *kube.Conn
	action    string
	tgt       string
	detail    map[string]any
	skipAudit bool
}

func (c *kctx) q(k string) string { return c.r.URL.Query().Get(k) }
func (c *kctx) body(v any) error  { return readJSON(c.r, v) }
func (c *kctx) name() string      { return c.r.PathValue("name") }

// ns is the namespace in the path ("_" for cluster-scoped objects).
func (c *kctx) ns() string {
	if ns := c.r.PathValue("ns"); ns != "_" {
		return ns
	}
	return ""
}

// nsAll is the namespace query parameter: "" or "*" = all namespaces.
func (c *kctx) nsAll() string {
	if ns := c.q("namespace"); ns != "*" {
		return ns
	}
	return ""
}

func (c *kctx) rt() (kube.ResourceType, error) { return c.conn.Resolve(c.ctx, c.r.PathValue("type")) }

func okv(err error) (any, error) { return map[string]bool{"ok": err == nil}, err }

type kubeFn func(c *kctx) (any, error)

func (s *Server) kubeCtx(w http.ResponseWriter, r *http.Request, ctx context.Context) (*kctx, bool) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return nil, false
	}
	cl := ws.Find("KubernetesCluster", r.PathValue("c"))
	if cl == nil {
		writeErr(w, 404, "cluster not found")
		return nil, false
	}
	conn, err := s.kube.Conn(ctx, ws, cl)
	if err != nil {
		kubeFail(w, err)
		return nil, false
	}
	return &kctx{ctx: ctx, r: r, ws: ws, cl: cl, conn: conn, detail: map[string]any{}}, true
}

func (s *Server) kubeRead(fn kubeFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		c, ok := s.kubeCtx(w, r, ctx)
		if !ok {
			return
		}
		v, err := fn(c)
		if err != nil {
			kubeFail(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
}

// kubeChange serves a change: audited, and it completes even when the page
// goes away.
func (s *Server) kubeChange(action string, fn kubeFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(connect.WithActor(context.WithoutCancel(r.Context()), connect.Actor{Kind: "user"}), 10*time.Minute)
		defer cancel()
		c, ok := s.kubeCtx(w, r, ctx)
		if !ok {
			return
		}
		c.action = action
		c.tgt = c.cl.ID()
		if t := r.PathValue("type"); t != "" {
			c.tgt += "/" + t + "/" + r.PathValue("ns") + "/" + r.PathValue("name")
		}
		v, err := fn(c)
		if !c.skipAudit {
			s.record(r, c.ws.Name(), c.action, c.tgt, c.detail, err)
		}
		if err != nil {
			kubeFail(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
}

// kubeFail maps API errors to statuses: the cluster's answer is a 4xx with
// its message.
func kubeFail(w http.ResponseWriter, err error) {
	var st apierrors.APIStatus
	switch {
	case apierrors.IsNotFound(err):
		writeErr(w, 404, err.Error())
	case apierrors.IsForbidden(err):
		writeErr(w, 403, err.Error())
	case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
		writeErr(w, 409, err.Error())
	case errors.As(err, &st):
		writeErr(w, 400, err.Error())
	case errors.Is(err, kube.ErrNoMetrics):
		writeErr(w, 404, err.Error())
	case strings.Contains(err.Error(), "vault"):
		fail(w, err)
	default:
		writeErr(w, 400, err.Error())
	}
}

// kubeLogStream streams the logs of a pod or of every pod of a workload
// (SSE, same format as the docker sources: {ts, src, line}).
func (s *Server) kubeLogStream(w http.ResponseWriter, r *http.Request) {
	c, ok := s.kubeCtx(w, r, r.Context())
	if !ok {
		return
	}
	q := r.URL.Query()
	rt, err := c.conn.Resolve(r.Context(), q.Get("type"))
	if err != nil {
		kubeFail(w, err)
		return
	}
	tail, err := strconv.ParseInt(q.Get("lines"), 10, 64)
	if q.Get("lines") == "all" {
		tail = -1
	} else if err != nil || tail <= 0 || tail > 50000 {
		tail = 500
	}
	follow := q.Get("follow") == "1"
	opts := kube.LogOptions{Container: q.Get("container"), Tail: tail, Since: q.Get("since"), Previous: q.Get("previous") == "1", Follow: follow}
	ns := q.Get("namespace")
	if ns == "" {
		ns = c.conn.NS
	}
	sse := newSSE(w)
	if sse == nil {
		return
	}
	defer sse.close()
	err = c.conn.StreamLogs(r.Context(), rt, ns, q.Get("name"), opts, func(l kube.LogLine) { sse.send("", l) })
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		sse.send("error", map[string]any{"message": err.Error(), "retry": follow})
		return
	}
	sse.send("end", map[string]bool{"ok": true})
}

// kubeEventStream sends the last hour of events, then new ones (SSE).
func (s *Server) kubeEventStream(w http.ResponseWriter, r *http.Request) {
	c, ok := s.kubeCtx(w, r, r.Context())
	if !ok {
		return
	}
	sse := newSSE(w)
	if sse == nil {
		return
	}
	defer sse.close()
	err := c.conn.WatchEvents(r.Context(), c.nsAll(), func(e kube.Event) { sse.send("", map[string]any{"event": e}) })
	if err != nil && r.Context().Err() == nil {
		sse.send("error", map[string]string{"message": err.Error()})
	}
}

// kubeExec opens a terminal in a pod's container over a WebSocket.
func (s *Server) kubeExec(w http.ResponseWriter, r *http.Request) {
	c, ok := s.kubeCtx(w, r, r.Context())
	if !ok {
		return
	}
	ns, pod := r.PathValue("ns"), r.PathValue("pod")
	q := r.URL.Query()
	var cmd []string
	switch sh := q.Get("shell"); sh {
	case "", "auto":
	case "sh", "bash", "ash", "zsh":
		cmd = []string{sh}
	default:
		writeErr(w, 400, "unsupported shell")
		return
	}
	s.ptyWS(w, r, c.ws, c.cl, "k8s.exec", c.cl.ID()+"/"+ns+"/"+pod+"/"+q.Get("container"), func(ctx context.Context, cols, rows int) (ptySession, error) {
		return c.conn.Shell(ns, pod, q.Get("container"), cmd, cols, rows)
	})
}

// sseWriter is a Server-Sent Events writer with keep-alive pings.
type sseWriter struct {
	w    http.ResponseWriter
	f    http.Flusher
	mu   sync.Mutex
	done chan struct{}
}

func newSSE(w http.ResponseWriter) *sseWriter {
	f, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return nil
	}
	sseHeaders(w)
	f.Flush()
	s := &sseWriter{w: w, f: f, done: make(chan struct{})}
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-t.C:
				s.mu.Lock()
				fmt.Fprint(w, ": ping\n\n")
				f.Flush()
				s.mu.Unlock()
			}
		}
	}()
	return s
}

func (s *sseWriter) send(event string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(v)
	if event != "" {
		fmt.Fprintf(s.w, "event: %s\n", event)
	}
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	s.f.Flush()
}

func (s *sseWriter) close() { close(s.done) }
