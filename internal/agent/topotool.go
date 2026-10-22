package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"opsdeck/internal/llm"
	"opsdeck/internal/topology"
)

// topoOp is one change to the topology.
type topoOp struct {
	Action string          `json:"action"`
	ID     string          `json:"id"`
	Node   json.RawMessage `json:"node"`
	Zone   json.RawMessage `json:"zone"`
	Link   json.RawMessage `json:"link"`
	YAML   string          `json:"yaml"`
}

// mergeJSON overlays the JSON fields of patch onto v (a struct pointer).
func mergeJSON(v any, patch json.RawMessage) error {
	if len(patch) == 0 {
		return nil
	}
	cur, _ := json.Marshal(v)
	var m map[string]any
	json.Unmarshal(cur, &m)
	var p map[string]any
	if err := json.Unmarshal(patch, &p); err != nil {
		return fmt.Errorf("invalid object: %v", err)
	}
	loosen(p)
	for k, x := range p {
		if x == nil {
			delete(m, k)
		} else {
			m[k] = x
		}
	}
	b, _ := json.Marshal(m)
	return json.Unmarshal(b, v)
}

// loosen accepts what models often send: numbers for ports and VLANs,
// a string for replicas, a single string for a list.
func loosen(m map[string]any) {
	for k, v := range m {
		switch k {
		case "port", "vlan", "bandwidth", "image", "label", "name":
			if f, ok := v.(float64); ok {
				m[k] = strconv.FormatFloat(f, 'f', -1, 64)
			}
		case "replicas":
			if s, ok := v.(string); ok {
				n, _ := strconv.Atoi(strings.TrimSpace(s))
				m[k] = float64(n)
			}
		case "ports", "tech", "addresses", "tags":
			switch x := v.(type) {
			case string:
				m[k] = []any{x}
			case float64:
				m[k] = []any{strconv.FormatFloat(x, 'f', -1, 64)}
			case []any:
				for i, e := range x {
					if f, ok := e.(float64); ok {
						x[i] = strconv.FormatFloat(f, 'f', -1, 64)
					}
				}
			}
		case "deployments":
			if list, ok := v.([]any); ok {
				for _, d := range list {
					if dm, ok := d.(map[string]any); ok {
						loosen(dm)
					}
				}
			}
		}
	}
}

func applyTopoOp(env *Env, t *topology.Topology, op topoOp) (string, error) {
	switch op.Action {
	case "add_node":
		var n topology.Node
		if err := mergeJSON(&n, op.Node); err != nil {
			return "", err
		}
		if n.Type == "" {
			return "", errors.New("add_node: node.type is required (see action=catalog)")
		}
		if n.ID == "" {
			n.ID = t.NewID(firstNonEmpty(n.Label, n.Type))
		}
		if t.Node(n.ID) != nil {
			return "", fmt.Errorf("node %s already exists (use update_node)", n.ID)
		}
		t.Nodes = append(t.Nodes, n)
		return "added node " + n.ID, nil
	case "update_node":
		n := t.Node(op.ID)
		if n == nil {
			return "", fmt.Errorf("node %q not found", op.ID)
		}
		id := n.ID
		if err := mergeJSON(n, op.Node); err != nil {
			return "", err
		}
		n.ID = id
		return "updated node " + id, nil
	case "remove_node":
		if !t.RemoveNode(op.ID) {
			return "", fmt.Errorf("node %q not found", op.ID)
		}
		return "removed node " + op.ID + " and its connections", nil
	case "add_zone":
		var z topology.Zone
		if err := mergeJSON(&z, op.Zone); err != nil {
			return "", err
		}
		if z.ID == "" {
			z.ID = t.NewID(firstNonEmpty(z.Label, z.Kind, "zone"))
		}
		if t.Zone(z.ID) != nil {
			return "", fmt.Errorf("zone %s already exists (use update_zone)", z.ID)
		}
		t.Zones = append(t.Zones, z)
		return "added zone " + z.ID, nil
	case "update_zone":
		z := t.Zone(op.ID)
		if z == nil {
			return "", fmt.Errorf("zone %q not found", op.ID)
		}
		id := z.ID
		if err := mergeJSON(z, op.Zone); err != nil {
			return "", err
		}
		z.ID = id
		return "updated zone " + id, nil
	case "remove_zone":
		if !t.RemoveZone(op.ID) {
			return "", fmt.Errorf("zone %q not found", op.ID)
		}
		return "removed zone " + op.ID + " (its contents moved up)", nil
	case "connect":
		var l topology.Link
		if err := mergeJSON(&l, op.Link); err != nil {
			return "", err
		}
		if l.From == "" || l.To == "" {
			return "", errors.New("connect: link.from and link.to are required")
		}
		if l.ID == "" {
			l.ID = t.NewID(l.From + "--" + l.To)
		}
		t.Links = append(t.Links, l)
		return "connected " + l.From + " → " + l.To + " [" + l.ID + "]", nil
	case "update_link":
		l := t.Link(op.ID)
		if l == nil {
			return "", fmt.Errorf("link %q not found", op.ID)
		}
		id := l.ID
		if err := mergeJSON(l, op.Link); err != nil {
			return "", err
		}
		l.ID = id
		return "updated link " + id, nil
	case "disconnect":
		for i, l := range t.Links {
			if l.ID == op.ID {
				t.Links = append(t.Links[:i], t.Links[i+1:]...)
				return "removed link " + op.ID, nil
			}
		}
		return "", fmt.Errorf("link %q not found", op.ID)
	case "import_inventory":
		added := topology.ImportInventory(t, env.WS)
		if len(added) == 0 {
			return "every inventory resource is already on the diagram", nil
		}
		return "added from the inventory: " + strings.Join(added, ", "), nil
	case "replace":
		var nt topology.Topology
		if err := yaml.Unmarshal([]byte(op.YAML), &nt); err != nil {
			return "", fmt.Errorf("invalid YAML: %v", err)
		}
		nt.Layout = t.Layout // keep positions of nodes that stay
		*t = nt
		return fmt.Sprintf("replaced the topology: %d zones, %d nodes, %d links", len(t.Zones), len(t.Nodes), len(t.Links)), nil
	}
	return "", fmt.Errorf("unknown action %q", op.Action)
}

func topoCatalog() string {
	var b strings.Builder
	b.WriteString("Node types (type: variants | well-known tech):\n")
	cat := ""
	for _, n := range topology.NodeTypes {
		if n.Category != cat {
			cat = n.Category
			b.WriteString("## " + cat + "\n")
		}
		fmt.Fprintf(&b, "- %s (%s)", n.Type, n.Label)
		if len(n.Variants) > 0 {
			b.WriteString(": " + strings.Join(n.Variants, ", "))
		}
		if len(n.Tech) > 0 {
			b.WriteString(" | " + strings.Join(n.Tech, ", "))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nLink kinds: ")
	var lk []string
	for _, l := range topology.LinkKinds {
		lk = append(lk, l.Kind)
	}
	b.WriteString(strings.Join(lk, ", "))
	b.WriteString("\nZone kinds: ")
	var zk []string
	for _, z := range topology.ZoneKinds {
		zk = append(zk, z.ID)
	}
	b.WriteString(strings.Join(zk, ", "))
	b.WriteString("\nDeployment kinds: ")
	var dk []string
	for _, d := range topology.DeploymentKinds {
		dk = append(dk, d.ID)
	}
	b.WriteString(strings.Join(dk, ", ") + "\n")
	return b.String()
}

func toolTopology() Tool {
	opProps := map[string]any{
		"action": enum("Action", "get", "catalog", "add_node", "update_node", "remove_node", "add_zone", "update_zone", "remove_zone",
			"connect", "update_link", "disconnect", "import_inventory", "replace", "batch"),
		"id":   str("update_*/remove_*/disconnect: the node, zone or link id"),
		"node": map[string]any{"type": "object", "description": "add_node / update_node fields: id, type (see catalog), variant, label, zone, vendor, tech[], resource (inventory ref, e.g. host/app-1), addresses[], environment, deployments[{name, kind, image, ports[], replicas, repo, path, notes}], tags[], notes. update_node merges; null removes a field."},
		"zone": map[string]any{"type": "object", "description": "add_zone / update_zone fields: id, label, kind (region, datacenter, cloud, vpc, subnet, vlan, dmz, office, cluster, namespace, environment), parent, cidr, provider, notes"},
		"link": map[string]any{"type": "object", "description": "connect / update_link fields: from, to (node ids), kind (https, http, grpc, tcp, sql, redis, kafka, replication, ipsec, wireguard, bgp, lan, wan, ssh, metrics…), port, label, encrypted, bidirectional, bandwidth, vlan, notes"},
		"yaml": str("replace: the complete topology YAML (zones, nodes, links)"),
	}
	props := map[string]any{}
	for k, v := range opProps {
		props[k] = v
	}
	props["format"] = enum("get: text (default) or yaml", "text", "yaml")
	props["operations"] = map[string]any{"type": "array", "description": "batch: several operations applied together (one approval)", "items": obj(opProps, "action")}
	return Tool{
		Def: llm.Tool{Name: "topology", Description: "The workspace's infrastructure topology (topology.yaml): zones (data centers, VPCs, subnets, DMZ, offices), components (routers of every kind, switches, firewalls, CDN, WAF, DDoS protection, load balancers, reverse proxies, DNS, VPN gateways, servers, VMs, hypervisors, Kubernetes, databases, caches, queues, storage, CI/CD, monitoring, identity…) with vendor, technologies and what is deployed on them, and the connections between them (protocol, port, encryption). Use get to understand where which tool or technology is used before working, and catalog for the available types. Changes (add/update/remove nodes, zones and links, import_inventory, replace, batch) always ask the user first — propose them when you learn something the diagram lacks or contradicts.",
			Schema: obj(props, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				topoOp
				Format     string   `json:"format"`
				Operations []topoOp `json:"operations"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			switch in.Action {
			case "get":
				return &Call{Risk: RiskRead, Target: "topology", Run: func(ctx context.Context) (string, error) {
					t, _, err := topology.Load(env.WS)
					if err != nil {
						return "", err
					}
					if in.Format == "yaml" {
						t.Layout = nil
						b, err := topology.Marshal(t)
						return string(b), err
					}
					return topology.Render(t, env.WS.Resources), nil
				}}, nil
			case "catalog":
				return &Call{Risk: RiskRead, Target: "topology", Run: func(ctx context.Context) (string, error) { return topoCatalog(), nil }}, nil
			}
			ops := []topoOp{in.topoOp}
			if in.Action == "batch" {
				if len(in.Operations) == 0 {
					return nil, errors.New("batch: operations is empty")
				}
				ops = in.Operations
			}
			apply := func() (*topology.Topology, string, []string, error) {
				t, rev, err := topology.Load(env.WS)
				if err != nil {
					return nil, "", nil, err
				}
				var msgs []string
				for _, op := range ops {
					if op.Action == "batch" || op.Action == "get" || op.Action == "catalog" {
						return nil, "", nil, fmt.Errorf("%s cannot be part of a batch", op.Action)
					}
					m, err := applyTopoOp(env, t, op)
					if err != nil {
						return nil, "", nil, err
					}
					msgs = append(msgs, m)
				}
				if err := t.Validate(); err != nil {
					return nil, "", nil, err
				}
				return t, rev, msgs, nil
			}
			before, _, err := topology.Load(env.WS)
			if err != nil {
				return nil, err
			}
			after, _, msgs, err := apply()
			if err != nil {
				return nil, err
			}
			diff := unifiedDiff("topology", topology.Render(before, nil), topology.Render(after, nil))
			return &Call{Risk: RiskChange, Target: "topology", Environment: "workspace topology", Protected: true, Match: in.Action,
				Reasons:    []string{"changes topology.yaml, the workspace's shared infrastructure diagram"},
				Grant:      "topology-edit",
				GrantLabel: "Always allow topology changes in this session",
				Preview:    strings.Join(msgs, "\n") + "\n\n" + diff,
				Run: func(ctx context.Context) (string, error) {
					t, rev, msgs, err := apply()
					if err != nil {
						return "", err
					}
					if _, err := topology.Save(env.WS, t, rev); err != nil {
						return "", err
					}
					return strings.Join(msgs, "\n") + "\nSaved topology.yaml; it shows on the Topology page.", nil
				}}, nil
		},
	}
}
