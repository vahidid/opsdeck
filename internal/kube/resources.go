package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// ResourceType is an API resource the cluster serves (built in or a CRD).
type ResourceType struct {
	ID         string   `json:"id"` // "deployments.apps", "pods", "certificates.cert-manager.io"
	Group      string   `json:"group"`
	Version    string   `json:"version"`
	Resource   string   `json:"resource"` // plural
	Kind       string   `json:"kind"`
	Namespaced bool     `json:"namespaced"`
	Verbs      []string `json:"verbs"`
	ShortNames []string `json:"shortNames,omitempty"`
	Category   string   `json:"category"`
}

// Can reports whether the resource supports a verb.
func (r ResourceType) Can(verb string) bool {
	for _, v := range r.Verbs {
		if v == verb {
			return true
		}
	}
	return false
}

// GVR is the resource's group/version/resource.
func (r ResourceType) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Resource}
}

// Path is the REST path of the collection (namespace "" = all namespaces or
// cluster scoped).
func (r ResourceType) Path(ns string) string {
	base := "/api/" + r.Version
	if r.Group != "" {
		base = "/apis/" + r.Group + "/" + r.Version
	}
	if r.Namespaced && ns != "" {
		return base + "/namespaces/" + ns + "/" + r.Resource
	}
	return base + "/" + r.Resource
}

// ObjectPath is the REST path of one object.
func (r ResourceType) ObjectPath(ns, name string) string {
	return r.Path(ns) + "/" + name
}

// The built-in resources the UI groups; anything else from a non-core
// group is a custom resource.
var categories = map[string]string{
	"pods": "Workloads", "deployments.apps": "Workloads", "statefulsets.apps": "Workloads", "daemonsets.apps": "Workloads",
	"replicasets.apps": "Workloads", "jobs.batch": "Workloads", "cronjobs.batch": "Workloads",
	"services": "Network", "ingresses.networking.k8s.io": "Network", "ingressclasses.networking.k8s.io": "Network",
	"networkpolicies.networking.k8s.io": "Network", "endpointslices.discovery.k8s.io": "Network", "endpoints": "Network",
	"gateways.gateway.networking.k8s.io": "Network", "httproutes.gateway.networking.k8s.io": "Network",
	"configmaps": "Config", "secrets": "Config", "horizontalpodautoscalers.autoscaling": "Config",
	"poddisruptionbudgets.policy": "Config", "resourcequotas": "Config", "limitranges": "Config",
	"priorityclasses.scheduling.k8s.io": "Config", "runtimeclasses.node.k8s.io": "Config",
	"persistentvolumeclaims": "Storage", "persistentvolumes": "Storage", "storageclasses.storage.k8s.io": "Storage",
	"volumeattachments.storage.k8s.io": "Storage", "csidrivers.storage.k8s.io": "Storage",
	"serviceaccounts": "Access", "roles.rbac.authorization.k8s.io": "Access", "rolebindings.rbac.authorization.k8s.io": "Access",
	"clusterroles.rbac.authorization.k8s.io": "Access", "clusterrolebindings.rbac.authorization.k8s.io": "Access",
	"nodes": "Cluster", "namespaces": "Cluster", "events": "Cluster", "customresourcedefinitions.apiextensions.k8s.io": "Cluster",
	"apiservices.apiregistration.k8s.io": "Cluster", "mutatingwebhookconfigurations.admissionregistration.k8s.io": "Cluster",
	"validatingwebhookconfigurations.admissionregistration.k8s.io": "Cluster", "leases.coordination.k8s.io": "Cluster",
}

// Order of the built-in types inside their category.
var order = []string{"pods", "deployments.apps", "statefulsets.apps", "daemonsets.apps", "replicasets.apps", "jobs.batch", "cronjobs.batch",
	"services", "ingresses.networking.k8s.io", "ingressclasses.networking.k8s.io", "networkpolicies.networking.k8s.io", "endpointslices.discovery.k8s.io",
	"gateways.gateway.networking.k8s.io", "httproutes.gateway.networking.k8s.io",
	"configmaps", "secrets", "horizontalpodautoscalers.autoscaling", "poddisruptionbudgets.policy", "resourcequotas", "limitranges",
	"persistentvolumeclaims", "persistentvolumes", "storageclasses.storage.k8s.io",
	"serviceaccounts", "roles.rbac.authorization.k8s.io", "rolebindings.rbac.authorization.k8s.io", "clusterroles.rbac.authorization.k8s.io",
	"clusterrolebindings.rbac.authorization.k8s.io", "nodes", "namespaces", "events", "customresourcedefinitions.apiextensions.k8s.io"}

// groups that hold built-in, rarely browsed resources (not "custom")
var systemGroups = map[string]bool{
	"": true, "apps": true, "batch": true, "autoscaling": true, "policy": true, "networking.k8s.io": true, "discovery.k8s.io": true,
	"rbac.authorization.k8s.io": true, "storage.k8s.io": true, "scheduling.k8s.io": true, "coordination.k8s.io": true, "node.k8s.io": true,
	"apiextensions.k8s.io": true, "apiregistration.k8s.io": true, "admissionregistration.k8s.io": true, "certificates.k8s.io": true,
	"authentication.k8s.io": true, "authorization.k8s.io": true, "events.k8s.io": true, "flowcontrol.apiserver.k8s.io": true,
	"metrics.k8s.io": true, "resource.k8s.io": true, "internal.apiserver.k8s.io": true, "storagemigration.k8s.io": true,
}

type discoveryCache struct {
	mu    sync.Mutex
	at    time.Time
	types []ResourceType
}

// Resources lists the resource types the cluster serves (cached for 5
// minutes; refresh forces a new discovery, e.g. after installing CRDs).
func (c *Conn) Resources(ctx context.Context, refresh bool) ([]ResourceType, error) {
	c.disc.mu.Lock()
	defer c.disc.mu.Unlock()
	if !refresh && c.disc.types != nil && time.Since(c.disc.at) < 5*time.Minute {
		return c.disc.types, nil
	}
	lists, err := c.CS.Discovery().ServerPreferredResources()
	if err != nil && len(lists) == 0 {
		return nil, err
	}
	// partial failures (an aggregated API down) still give the rest
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return nil, err
	}
	out := []ResourceType{}
	for _, l := range lists {
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") { // subresources
				continue
			}
			id := r.Name
			if gv.Group != "" {
				id += "." + gv.Group
			}
			rt := ResourceType{ID: id, Group: gv.Group, Version: gv.Version, Resource: r.Name, Kind: r.Kind,
				Namespaced: r.Namespaced, Verbs: r.Verbs, ShortNames: r.ShortNames}
			if !rt.Can("list") {
				continue
			}
			switch {
			case categories[id] != "":
				rt.Category = categories[id]
			case !systemGroups[gv.Group]:
				rt.Category = "Custom resources"
			default:
				rt.Category = "Other"
			}
			out = append(out, rt)
		}
	}
	rank := map[string]int{}
	for i, id := range order {
		rank[id] = i + 1
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := rank[out[i].ID], rank[out[j].ID]
		if a != 0 || b != 0 {
			if a == 0 {
				return false
			}
			if b == 0 {
				return true
			}
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	c.disc.types, c.disc.at = out, time.Now()
	return out, nil
}

// Resolve finds a resource type by id ("deployments.apps"), plural
// ("deployments"), singular, kind ("Deployment") or short name ("deploy").
func (c *Conn) Resolve(ctx context.Context, name string) (ResourceType, error) {
	types, err := c.Resources(ctx, false)
	if err != nil {
		return ResourceType{}, err
	}
	if rt, ok := find(types, name); ok {
		return rt, nil
	}
	// maybe a CRD installed since the last discovery
	if types, err = c.Resources(ctx, true); err == nil {
		if rt, ok := find(types, name); ok {
			return rt, nil
		}
	}
	return ResourceType{}, fmt.Errorf("the cluster has no resource type %q", name)
}

func find(types []ResourceType, name string) (ResourceType, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, t := range types {
		if t.ID == n {
			return t, true
		}
	}
	// plural / kind / short name, preferring built-in groups
	var hit *ResourceType
	for i := range types {
		t := &types[i]
		match := t.Resource == n || strings.ToLower(t.Kind) == n || strings.TrimSuffix(t.Resource, "s") == n
		for _, s := range t.ShortNames {
			match = match || s == n
		}
		if match && (hit == nil || (systemGroups[t.Group] && !systemGroups[hit.Group])) {
			hit = t
		}
	}
	if hit != nil {
		return *hit, true
	}
	return ResourceType{}, false
}

// ForKind finds the resource type for an apiVersion and kind (for apply).
func (c *Conn) ForKind(ctx context.Context, apiVersion, kind string) (ResourceType, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return ResourceType{}, err
	}
	for _, refresh := range []bool{false, true} {
		types, err := c.Resources(ctx, refresh)
		if err != nil {
			return ResourceType{}, err
		}
		for _, t := range types {
			if t.Group == gv.Group && t.Kind == kind {
				t.Version = gv.Version // the manifest's version
				return t, nil
			}
		}
		// discovery only lists preferred versions and resources that can be
		// listed; look the kind up in its group version directly
		if l, err := c.CS.Discovery().ServerResourcesForGroupVersion(apiVersion); err == nil {
			for _, r := range l.APIResources {
				if r.Kind == kind && !strings.Contains(r.Name, "/") {
					return ResourceType{ID: r.Name + dotGroup(gv.Group), Group: gv.Group, Version: gv.Version, Resource: r.Name, Kind: kind,
						Namespaced: r.Namespaced, Verbs: r.Verbs}, nil
				}
			}
		}
	}
	return ResourceType{}, fmt.Errorf("the cluster does not know %s %s (missing CRD?)", apiVersion, kind)
}

func dotGroup(g string) string {
	if g == "" {
		return ""
	}
	return "." + g
}
