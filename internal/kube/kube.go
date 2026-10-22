// Package kube talks to Kubernetes clusters defined in a workspace. The
// kubeconfig stays in the vault (never written to disk) and API traffic goes
// through the cluster's via chain (VPN / bastion) when set.
package kube

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"opsdeck/internal/connect"
	"opsdeck/internal/vault"
	"opsdeck/internal/vpn"
	"opsdeck/internal/workspace"
)

type Manager struct {
	hub   *connect.Hub
	vault *vault.Vault

	mu      sync.Mutex
	clients map[string]*Conn
}

// Conn is a connection to one cluster: a typed clientset, the REST config
// (for exec, port-forward and raw requests) and the default namespace.
type Conn struct {
	CS  *kubernetes.Clientset
	RC  *rest.Config
	NS  string
	rev string

	socks *vpn.SocksServer // via: API traffic through the workspace's path
	disc  discoveryCache
}

func init() {
	// client-go reports normal closes of exec and port-forward streams as
	// errors on stderr; opsdeck surfaces the errors that matter itself.
	klog.SetOutput(io.Discard)
	klog.LogToStderr(false)
	klog.SetLogger(logr.Discard())
}

func New(hub *connect.Hub, v *vault.Vault) *Manager {
	m := &Manager{hub: hub, vault: v, clients: map[string]*Conn{}}
	v.OnLock(m.Reset)
	return m
}

// Reset drops all cached clients.
func (m *Manager) Reset() {
	m.mu.Lock()
	for _, c := range m.clients {
		c.close()
	}
	m.clients = map[string]*Conn{}
	m.mu.Unlock()
}

func (c *Conn) close() {
	if c.socks != nil {
		c.socks.Close()
	}
}

// Client returns a clientset and default namespace for a KubernetesCluster resource.
func (m *Manager) Client(ctx context.Context, w *workspace.Workspace, c *workspace.Resource) (*kubernetes.Clientset, string, error) {
	conn, err := m.Conn(ctx, w, c)
	if err != nil {
		return nil, "", err
	}
	return conn.CS, conn.NS, nil
}

// Conn returns the (cached) connection to a KubernetesCluster resource.
func (m *Manager) Conn(ctx context.Context, w *workspace.Workspace, c *workspace.Resource) (*Conn, error) {
	if c.Kind != "KubernetesCluster" {
		return nil, fmt.Errorf("%s is not a Kubernetes cluster", c.ID())
	}
	ref := c.Str("kubeconfig")
	it, _, err := m.vault.ResolveFor(w.Name(), ref)
	if err != nil {
		return nil, err
	}
	if a := connect.ActorFrom(ctx); a.Kind == "agent" && !it.Policy.AgentUse {
		return nil, fmt.Errorf("credential %s is not allowed for agent use", ref)
	}
	key := w.Name() + "/" + c.Name
	rev := fmt.Sprintf("%s:%d:%s:%s:%s", it.ID, it.Revision, c.Str("context"), c.Via, c.Str("namespace"))
	m.mu.Lock()
	if cc, ok := m.clients[key]; ok && cc.rev == rev {
		m.mu.Unlock()
		return cc, nil
	}
	m.mu.Unlock()

	raw, err := clientcmd.Load([]byte(it.Get("kubeconfig")))
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	overrides := &clientcmd.ConfigOverrides{}
	if ctxName := c.Str("context"); ctxName != "" {
		overrides.CurrentContext = ctxName
	} else if def := it.Get("context"); def != "" {
		overrides.CurrentContext = def
	}
	cc := clientcmd.NewDefaultClientConfig(*raw, overrides)
	rc, err := cc.ClientConfig()
	if err != nil {
		return nil, err
	}
	ns := c.Str("namespace")
	if ns == "" {
		ns, _, _ = cc.Namespace()
	}
	if ns == "" {
		ns = "default"
	}
	conn := &Conn{RC: rc, NS: ns, rev: rev}
	if c.Via != "" {
		// Background context: the dialer outlives this request. Exec and
		// port-forward upgrade connections ignore rest.Config.Dial but honour
		// a proxy, so every request goes through a local authenticated SOCKS5
		// proxy that dials through the workspace's path (VPN, bastion).
		dial, err := m.hub.Dialer(context.Background(), w, c)
		if err != nil {
			return nil, err
		}
		user, pass := randToken(), randToken()
		srv, err := vpn.ServeSOCKSAuth(0, vpn.DialFunc(dial), user, pass)
		if err != nil {
			return nil, err
		}
		conn.socks = srv
		proxyURL := &url.URL{Scheme: "socks5", User: url.UserPassword(user, pass), Host: srv.Addr()}
		rc.Proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	}
	rc.Timeout = 30 * time.Second
	rc.QPS, rc.Burst = 50, 100
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		conn.close()
		return nil, err
	}
	conn.CS = cs
	m.mu.Lock()
	if old, ok := m.clients[key]; ok {
		old.close()
	}
	m.clients[key] = conn
	m.mu.Unlock()
	return conn, nil
}

func randToken() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func evTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

// RolloutStatus summarizes a deployment's progress.
func RolloutStatus(ctx context.Context, cs kubernetes.Interface, ns, name string) (string, error) {
	d, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return rolloutText(d), nil
}

func rolloutText(d *appsv1.Deployment) string {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	var conds []string
	for _, c := range d.Status.Conditions {
		conds = append(conds, fmt.Sprintf("%s=%s (%s)", c.Type, c.Status, c.Reason))
	}
	return fmt.Sprintf("replicas wanted=%d updated=%d ready=%d available=%d; generation=%d observed=%d; %s",
		want, d.Status.UpdatedReplicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas,
		d.Generation, d.Status.ObservedGeneration, strings.Join(conds, ", "))
}
