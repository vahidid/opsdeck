package kube

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

// LogOptions say which part of the logs to stream.
type LogOptions struct {
	Container string // "" = every container
	Tail      int64  // lines per container; < 0 = all
	Since     string // duration (15m, 2h, 1d) or RFC3339 time
	Previous  bool   // the previous (crashed) container instance
	Follow    bool
}

// LogLine is a line of a pod's log.
type LogLine struct {
	TS   string `json:"ts,omitempty"`
	Src  string `json:"src,omitempty"` // pod and/or container, when several stream together
	Line string `json:"line"`
}

var sinceRe = regexp.MustCompile(`^([0-9]{1,6})([smhd])$`)

func applySince(o *corev1.PodLogOptions, since string) error {
	if since == "" {
		return nil
	}
	if m := sinceRe.FindStringSubmatch(since); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		mult := map[string]int64{"s": 1, "m": 60, "h": 3600, "d": 86400}[m[2]]
		secs := n * mult
		o.SinceSeconds = &secs
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, since)
	if err != nil {
		return fmt.Errorf("invalid since %q (e.g. 15m, 2h, 1d or an RFC3339 time)", since)
	}
	mt := metav1.NewTime(t)
	o.SinceTime = &mt
	return nil
}

// fixed-width timestamps sort as text
const tsFixed = "2006-01-02T15:04:05.000000000Z07:00"

func splitTS(s string) (string, string) {
	i := strings.IndexByte(s, ' ')
	if i < 20 || s[4] != '-' || s[10] != 'T' {
		return "", s
	}
	t, err := time.Parse(time.RFC3339Nano, s[:i])
	if err != nil {
		return "", s
	}
	return t.UTC().Format(tsFixed), s[i+1:]
}

// PodsFor returns the pods of an object: itself for a pod; its selector's
// pods for workloads, replicasets, jobs and services; the pods of a node.
func (c *Conn) PodsFor(ctx context.Context, rt ResourceType, ns, name string) ([]corev1.Pod, error) {
	switch rt.ID {
	case "pods":
		p, err := c.CS.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return []corev1.Pod{*p}, nil
	case "nodes":
		l, err := c.CS.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + name})
		if err != nil {
			return nil, err
		}
		return l.Items, nil
	case "cronjobs.batch":
		jobs, err := c.CS.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		var out []corev1.Pod
		for _, j := range jobs.Items {
			for _, ref := range j.OwnerReferences {
				if ref.Kind == "CronJob" && ref.Name == name {
					l, err := c.CS.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + j.Name})
					if err == nil {
						out = append(out, l.Items...)
					}
				}
			}
		}
		return out, nil
	}
	o, err := c.Get(ctx, rt, ns, name)
	if err != nil {
		return nil, err
	}
	sel, err := Selector(o)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %v", rt.Resource, name, err)
	}
	l, err := c.CS.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, err
	}
	sort.Slice(l.Items, func(i, j int) bool { return l.Items[i].Name < l.Items[j].Name })
	return l.Items, nil
}

// StreamLogs streams the logs of an object's pods (every container, or one)
// with docker-style labels when several streams are merged. Following a
// workload, pods that appear later (a rollout, a scale-up) join the stream.
func (c *Conn) StreamLogs(ctx context.Context, rt ResourceType, ns, name string, o LogOptions, onLine func(LogLine)) error {
	pods, err := c.PodsFor(ctx, rt, ns, name)
	if err != nil {
		return err
	}
	if len(pods) == 0 && !o.Follow {
		return errors.New("no pods")
	}
	var mu sync.Mutex
	emit := func(l LogLine) {
		mu.Lock()
		onLine(l)
		mu.Unlock()
	}
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	started := map[string]bool{}
	multiPod := rt.ID != "pods"
	start := func(p corev1.Pod, fresh bool) {
		containers := []string{}
		for _, ct := range p.Spec.InitContainers {
			if o.Container == ct.Name {
				containers = append(containers, ct.Name)
			}
		}
		for _, ct := range p.Spec.Containers {
			if o.Container == "" || o.Container == ct.Name {
				containers = append(containers, ct.Name)
			}
		}
		for _, ct := range containers {
			key := p.Namespace + "/" + p.Name + "/" + ct
			if started[key] {
				continue
			}
			started[key] = true
			src := ""
			switch {
			case multiPod && len(containers) > 1:
				src = p.Name + "/" + ct
			case multiPod:
				src = p.Name
			case len(p.Spec.Containers) > 1 && o.Container == "":
				src = ct
			}
			opts := &corev1.PodLogOptions{Container: ct, Follow: o.Follow, Timestamps: true, Previous: o.Previous}
			if !fresh {
				if o.Tail >= 0 {
					t := o.Tail
					opts.TailLines = &t
				}
				if err := applySince(opts, o.Since); err != nil {
					errMu.Lock()
					firstErr = err
					errMu.Unlock()
					return
				}
			}
			wg.Add(1)
			go func(pod string, opts *corev1.PodLogOptions, src string) {
				defer wg.Done()
				err := c.podLog(ctx, p.Namespace, pod, opts, func(raw string) {
					ts, line := splitTS(raw)
					emit(LogLine{TS: ts, Src: src, Line: line})
				})
				if err != nil && ctx.Err() == nil {
					// a pod that is still starting has no logs yet: retry it later
					if o.Follow && strings.Contains(err.Error(), "waiting to start") {
						mu.Lock()
						delete(started, key)
						mu.Unlock()
						return
					}
					if !multiPod {
						errMu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						errMu.Unlock()
					} else {
						emit(LogLine{Src: src, Line: "opsdeck: " + err.Error()})
					}
				}
			}(p.Name, opts, src)
		}
	}
	mu.Lock()
	for _, p := range pods {
		start(p, false)
	}
	mu.Unlock()
	if o.Follow && multiPod {
		go func() {
			t := time.NewTicker(3 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
				pods, err := c.PodsFor(ctx, rt, ns, name)
				if err != nil {
					continue
				}
				mu.Lock()
				for _, p := range pods {
					if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
						start(p, true)
					}
				}
				mu.Unlock()
			}
		}()
	}
	if o.Follow {
		<-ctx.Done()
	}
	wg.Wait()
	errMu.Lock()
	defer errMu.Unlock()
	return firstErr
}

func (c *Conn) podLog(ctx context.Context, ns, pod string, opts *corev1.PodLogOptions, onLine func(string)) error {
	stream, err := c.CS.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		onLine(sc.Text())
	}
	if ctx.Err() != nil {
		return nil
	}
	return sc.Err()
}

// WatchEvents sends the recent events (the last hour) of a namespace ("" =
// all), then new ones as they happen, until ctx ends.
func (c *Conn) WatchEvents(ctx context.Context, ns string, onEvent func(Event)) error {
	l, err := c.CS.CoreV1().Events(ns).List(ctx, metav1.ListOptions{Limit: 1000})
	if err != nil {
		return err
	}
	cut := time.Now().Add(-time.Hour)
	recent := []Event{}
	for i := range l.Items {
		if evTime(l.Items[i]).After(cut) {
			recent = append(recent, toEvent(&l.Items[i]))
		}
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].Time < recent[j].Time })
	for _, e := range recent {
		onEvent(e)
	}
	rv := l.ResourceVersion
	for ctx.Err() == nil {
		w, err := c.CS.CoreV1().Events(ns).Watch(ctx, metav1.ListOptions{ResourceVersion: rv, AllowWatchBookmarks: true})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for ev := range w.ResultChan() {
			switch ev.Type {
			case watch.Added, watch.Modified:
				if e, ok := ev.Object.(*corev1.Event); ok {
					rv = e.ResourceVersion
					onEvent(toEvent(e))
				}
			case watch.Bookmark:
				if e, ok := ev.Object.(*corev1.Event); ok {
					rv = e.ResourceVersion
				}
			case watch.Error:
				rv = "" // expired: start from now
			}
		}
		w.Stop()
	}
	return nil
}
