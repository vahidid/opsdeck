// Package topology is the workspace's infrastructure diagram: zones (data
// centers, VPCs, subnets, DMZs, offices…), nodes (routers, switches,
// firewalls, CDNs, WAFs, load balancers, servers, clusters, databases,
// CI/CD…, with vendor, technologies and what is deployed on them) and links
// (how they talk: HTTPS, BGP, IPsec, SQL, replication…). It is stored as
// topology.yaml in the workspace folder, edited on the Topology page and by
// the agent, and rendered as text for the agent's context.
package topology

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"opsdeck/internal/workspace"
)

// FileName is the topology file in the workspace folder.
const FileName = "topology.yaml"

// Topology is the whole diagram.
type Topology struct {
	Version int    `yaml:"version" json:"version"`
	Zones   []Zone `yaml:"zones,omitempty" json:"zones"`
	Nodes   []Node `yaml:"nodes,omitempty" json:"nodes"`
	Links   []Link `yaml:"links,omitempty" json:"links"`
	// Layout holds editor positions (and zone sizes); not meaningful to
	// the agent.
	Layout map[string]Box `yaml:"layout,omitempty" json:"layout"`
}

// Zone groups nodes: a region, data center, VPC, subnet, VLAN, DMZ, office…
type Zone struct {
	ID       string `yaml:"id" json:"id"`
	Label    string `yaml:"label,omitempty" json:"label,omitempty"`
	Kind     string `yaml:"kind,omitempty" json:"kind,omitempty"`
	Parent   string `yaml:"parent,omitempty" json:"parent,omitempty"`
	CIDR     string `yaml:"cidr,omitempty" json:"cidr,omitempty"`
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"` // AWS, Hetzner, on-prem…
	Notes    string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Node is a component.
type Node struct {
	ID          string       `yaml:"id" json:"id"`
	Type        string       `yaml:"type" json:"type"`
	Variant     string       `yaml:"variant,omitempty" json:"variant,omitempty"`
	Label       string       `yaml:"label,omitempty" json:"label,omitempty"`
	Zone        string       `yaml:"zone,omitempty" json:"zone,omitempty"`
	Vendor      string       `yaml:"vendor,omitempty" json:"vendor,omitempty"` // product / vendor, e.g. MikroTik CCR2004, Cloudflare
	Tech        []string     `yaml:"tech,omitempty" json:"tech,omitempty"`     // technologies used: RouterOS 7, BGP, nginx 1.25…
	Resource    string       `yaml:"resource,omitempty" json:"resource,omitempty"`
	Addresses   []string     `yaml:"addresses,omitempty" json:"addresses,omitempty"`
	Environment string       `yaml:"environment,omitempty" json:"environment,omitempty"`
	Deployments []Deployment `yaml:"deployments,omitempty" json:"deployments,omitempty"`
	Tags        []string     `yaml:"tags,omitempty" json:"tags,omitempty"`
	Notes       string       `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Deployment is something running on a node.
type Deployment struct {
	Name     string   `yaml:"name" json:"name"`
	Kind     string   `yaml:"kind,omitempty" json:"kind,omitempty"`   // docker-compose, kubernetes, systemd…
	Image    string   `yaml:"image,omitempty" json:"image,omitempty"` // image, package or version
	Ports    []string `yaml:"ports,omitempty" json:"ports,omitempty"`
	Replicas int      `yaml:"replicas,omitempty" json:"replicas,omitempty"`
	Repo     string   `yaml:"repo,omitempty" json:"repo,omitempty"` // git/<name> or URL of its deployment files
	Path     string   `yaml:"path,omitempty" json:"path,omitempty"` // where it lives on the node
	Notes    string   `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Link is a connection between two nodes.
type Link struct {
	ID            string `yaml:"id" json:"id"`
	From          string `yaml:"from" json:"from"`
	To            string `yaml:"to" json:"to"`
	Kind          string `yaml:"kind,omitempty" json:"kind,omitempty"`
	Label         string `yaml:"label,omitempty" json:"label,omitempty"`
	Port          string `yaml:"port,omitempty" json:"port,omitempty"`
	Encrypted     bool   `yaml:"encrypted,omitempty" json:"encrypted,omitempty"`
	Bidirectional bool   `yaml:"bidirectional,omitempty" json:"bidirectional,omitempty"`
	Bandwidth     string `yaml:"bandwidth,omitempty" json:"bandwidth,omitempty"`
	VLAN          string `yaml:"vlan,omitempty" json:"vlan,omitempty"`
	Notes         string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Box is a position (and size, for zones) on the canvas.
type Box struct {
	X float64 `yaml:"x" json:"x"`
	Y float64 `yaml:"y" json:"y"`
	W float64 `yaml:"w,omitempty" json:"w,omitempty"`
	H float64 `yaml:"h,omitempty" json:"h,omitempty"`
}

const header = `# Topology of this workspace: zones, components (nodes), what runs on them
# (deployments) and how they connect (links). Edited on opsdeck's Topology page
# and by the agent (topology tool). "layout" only holds positions on the canvas.
`

// Path of the topology file of w.
func Path(w *workspace.Workspace) string { return filepath.Join(w.Path, FileName) }

// Load reads the topology (empty when there is none) and its revision.
func Load(w *workspace.Workspace) (*Topology, string, error) {
	b, err := os.ReadFile(Path(w))
	if errors.Is(err, os.ErrNotExist) {
		return &Topology{Version: 1}, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var t Topology
	if err := yaml.Unmarshal(b, &t); err != nil {
		return nil, "", fmt.Errorf("%s: %w", FileName, err)
	}
	t.normalize()
	return &t, rev(b), nil
}

func rev(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// ErrConflict means the file changed since it was read.
var ErrConflict = errors.New("the topology was changed meanwhile (by the agent or another tab): reload it")

// Save validates and writes t. When expect is not empty it must match the
// current revision. Returns the new revision.
func Save(w *workspace.Workspace, t *Topology, expect string) (string, error) {
	t.normalize()
	if err := t.Validate(); err != nil {
		return "", err
	}
	if expect != "" {
		if b, err := os.ReadFile(Path(w)); err == nil && rev(b) != expect {
			return "", ErrConflict
		}
	}
	out, err := Marshal(t)
	if err != nil {
		return "", err
	}
	tmp := Path(w) + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, Path(w)); err != nil {
		return "", err
	}
	return rev(out), nil
}

// Marshal is the file content of t.
func Marshal(t *Topology) ([]byte, error) {
	b, err := yaml.Marshal(t)
	if err != nil {
		return nil, err
	}
	return append([]byte(header), b...), nil
}

// Empty tells whether there is nothing on the diagram.
func (t *Topology) Empty() bool { return len(t.Nodes) == 0 && len(t.Zones) == 0 }

func (t *Topology) normalize() {
	if t.Version == 0 {
		t.Version = 1
	}
	if t.Zones == nil {
		t.Zones = []Zone{}
	}
	if t.Nodes == nil {
		t.Nodes = []Node{}
	}
	if t.Links == nil {
		t.Links = []Link{}
	}
	for i := range t.Links {
		if t.Links[i].ID == "" {
			t.Links[i].ID = t.newLinkID(t.Links[i].From, t.Links[i].To)
		}
	}
	// Positions of things that no longer exist go away.
	if t.Layout != nil {
		for id := range t.Layout {
			if t.Node(id) == nil && t.Zone(id) == nil {
				delete(t.Layout, id)
			}
		}
	}
}

var idRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,80}$`)

// Validate checks IDs and references.
func (t *Topology) Validate() error {
	seen := map[string]string{}
	var errs []string
	use := func(id, what string) {
		if !idRe.MatchString(id) {
			errs = append(errs, fmt.Sprintf("%s id %q: use letters, digits, - _ . (no spaces)", what, id))
		} else if prev, dup := seen[id]; dup {
			errs = append(errs, fmt.Sprintf("id %q is used by a %s and a %s", id, prev, what))
		}
		seen[id] = what
	}
	for _, z := range t.Zones {
		use(z.ID, "zone")
	}
	for _, n := range t.Nodes {
		use(n.ID, "node")
		if n.Type == "" {
			errs = append(errs, fmt.Sprintf("node %s has no type", n.ID))
		}
		if n.Zone != "" && t.Zone(n.Zone) == nil {
			errs = append(errs, fmt.Sprintf("node %s: zone %q does not exist", n.ID, n.Zone))
		}
	}
	for _, z := range t.Zones {
		if z.Parent == "" {
			continue
		}
		if t.Zone(z.Parent) == nil {
			errs = append(errs, fmt.Sprintf("zone %s: parent %q does not exist", z.ID, z.Parent))
			continue
		}
		// no cycles
		for cur, n := z.Parent, 0; cur != ""; n++ {
			if cur == z.ID || n > len(t.Zones) {
				errs = append(errs, fmt.Sprintf("zone %s is inside itself", z.ID))
				break
			}
			cur = t.Zone(cur).Parent
		}
	}
	for _, l := range t.Links {
		use(l.ID, "link")
		if t.Node(l.From) == nil {
			errs = append(errs, fmt.Sprintf("link %s: node %q does not exist", l.ID, l.From))
		}
		if t.Node(l.To) == nil {
			errs = append(errs, fmt.Sprintf("link %s: node %q does not exist", l.ID, l.To))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (t *Topology) Node(id string) *Node {
	for i := range t.Nodes {
		if t.Nodes[i].ID == id {
			return &t.Nodes[i]
		}
	}
	return nil
}

func (t *Topology) Zone(id string) *Zone {
	for i := range t.Zones {
		if t.Zones[i].ID == id {
			return &t.Zones[i]
		}
	}
	return nil
}

func (t *Topology) Link(id string) *Link {
	for i := range t.Links {
		if t.Links[i].ID == id {
			return &t.Links[i]
		}
	}
	return nil
}

func (t *Topology) newLinkID(from, to string) string {
	base := from + "--" + to
	if !idRe.MatchString(base) {
		base = "link"
	}
	id := base
	for n := 2; t.Link(id) != nil || t.Node(id) != nil || t.Zone(id) != nil; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// NewID derives a free ID from a name.
func (t *Topology) NewID(name string) string {
	base := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "node"
	}
	id := base
	for n := 2; t.Node(id) != nil || t.Zone(id) != nil || t.Link(id) != nil; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

// RemoveNode deletes a node and its links.
func (t *Topology) RemoveNode(id string) bool {
	for i, n := range t.Nodes {
		if n.ID == id {
			t.Nodes = append(t.Nodes[:i], t.Nodes[i+1:]...)
			links := t.Links[:0]
			for _, l := range t.Links {
				if l.From != id && l.To != id {
					links = append(links, l)
				}
			}
			t.Links = links
			delete(t.Layout, id)
			return true
		}
	}
	return false
}

// RemoveZone deletes a zone; its nodes and child zones move to its parent.
func (t *Topology) RemoveZone(id string) bool {
	z := t.Zone(id)
	if z == nil {
		return false
	}
	parent := z.Parent
	for i := range t.Nodes {
		if t.Nodes[i].Zone == id {
			t.Nodes[i].Zone = parent
		}
	}
	for i := range t.Zones {
		if t.Zones[i].Parent == id {
			t.Zones[i].Parent = parent
		}
	}
	for i, x := range t.Zones {
		if x.ID == id {
			t.Zones = append(t.Zones[:i], t.Zones[i+1:]...)
			break
		}
	}
	delete(t.Layout, id)
	return true
}

// ----------------------------------------------------------------------------
// Rendering for the agent

func (t *Topology) zonePath(id string) string {
	var parts []string
	for cur, n := id, 0; cur != "" && n <= len(t.Zones); n++ {
		z := t.Zone(cur)
		if z == nil {
			break
		}
		parts = append([]string{firstNonEmpty(z.Label, z.ID)}, parts...)
		cur = z.Parent
	}
	return strings.Join(parts, " › ")
}

func (n Node) title() string {
	nt, _ := TypeOf(n.Type)
	s := nt.Label
	if n.Variant != "" {
		s += ", " + n.Variant
	}
	return s
}

// Render describes the topology as compact text for the agent.
func Render(t *Topology, inv []*workspace.Resource) string {
	if t.Empty() {
		return "The topology diagram is empty."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d zones, %d components, %d connections (topology.yaml).\n", len(t.Zones), len(t.Nodes), len(t.Links))
	byZone := map[string][]Node{}
	for _, n := range t.Nodes {
		byZone[n.Zone] = append(byZone[n.Zone], n)
	}
	// zones in tree order
	var order []string
	var walk func(parent string)
	walk = func(parent string) {
		for _, z := range t.Zones {
			if z.Parent == parent {
				order = append(order, z.ID)
				walk(z.ID)
			}
		}
	}
	walk("")
	order = append(order, "")
	for _, zid := range order {
		nodes := byZone[zid]
		if zid == "" && len(nodes) == 0 {
			continue
		}
		if zid == "" {
			b.WriteString("\n### Not in a zone\n")
		} else {
			z := t.Zone(zid)
			fmt.Fprintf(&b, "\n### Zone %s [%s]", t.zonePath(zid), z.ID)
			var attrs []string
			if z.Kind != "" {
				attrs = append(attrs, optionLabel(ZoneKinds, z.Kind))
			}
			if z.CIDR != "" {
				attrs = append(attrs, z.CIDR)
			}
			if z.Provider != "" {
				attrs = append(attrs, z.Provider)
			}
			if len(attrs) > 0 {
				b.WriteString(" — " + strings.Join(attrs, ", "))
			}
			b.WriteString("\n")
			if z.Notes != "" {
				b.WriteString("  " + oneLine(z.Notes) + "\n")
			}
		}
		for _, n := range nodes {
			fmt.Fprintf(&b, "- [%s] %s: %s", n.ID, firstNonEmpty(n.Label, n.ID), n.title())
			var attrs []string
			if n.Vendor != "" {
				attrs = append(attrs, n.Vendor)
			}
			if len(n.Tech) > 0 {
				attrs = append(attrs, strings.Join(n.Tech, ", "))
			}
			if len(n.Addresses) > 0 {
				attrs = append(attrs, "addr "+strings.Join(n.Addresses, ", "))
			}
			if n.Environment != "" {
				attrs = append(attrs, "env "+n.Environment)
			}
			if n.Resource != "" {
				attrs = append(attrs, "inventory "+n.Resource)
			}
			if len(n.Tags) > 0 {
				attrs = append(attrs, "tags "+strings.Join(n.Tags, ","))
			}
			if len(attrs) > 0 {
				b.WriteString(" — " + strings.Join(attrs, "; "))
			}
			b.WriteString("\n")
			for _, d := range n.Deployments {
				fmt.Fprintf(&b, "  - runs %s", d.Name)
				var a []string
				if d.Kind != "" {
					a = append(a, optionLabel(DeploymentKinds, d.Kind))
				}
				if d.Image != "" {
					a = append(a, d.Image)
				}
				if d.Replicas > 0 {
					a = append(a, fmt.Sprintf("%d replicas", d.Replicas))
				}
				if len(d.Ports) > 0 {
					a = append(a, "ports "+strings.Join(d.Ports, ","))
				}
				if d.Path != "" {
					a = append(a, "at "+d.Path)
				}
				if d.Repo != "" {
					a = append(a, "from "+d.Repo)
				}
				if len(a) > 0 {
					b.WriteString(" (" + strings.Join(a, ", ") + ")")
				}
				if d.Notes != "" {
					b.WriteString(" — " + oneLine(d.Notes))
				}
				b.WriteString("\n")
			}
			if n.Notes != "" {
				b.WriteString("  note: " + oneLine(n.Notes) + "\n")
			}
		}
	}
	if len(t.Links) > 0 {
		b.WriteString("\n### Connections\n")
		for _, l := range t.Links {
			arrow := "→"
			if l.Bidirectional {
				arrow = "↔"
			}
			lk, _ := linkKind(l.Kind)
			fmt.Fprintf(&b, "- %s %s %s: %s", l.From, arrow, l.To, firstNonEmpty(lk.Label, "connection"))
			var a []string
			if l.Port != "" {
				a = append(a, "port "+l.Port)
			}
			if l.Encrypted {
				a = append(a, "encrypted")
			}
			if l.Bandwidth != "" {
				a = append(a, l.Bandwidth)
			}
			if l.VLAN != "" {
				a = append(a, "VLAN "+l.VLAN)
			}
			if len(a) > 0 {
				b.WriteString(" (" + strings.Join(a, ", ") + ")")
			}
			if l.Label != "" {
				b.WriteString(" — " + oneLine(l.Label))
			}
			if l.Notes != "" {
				b.WriteString(" — " + oneLine(l.Notes))
			}
			b.WriteString(" [" + l.ID + "]\n")
		}
	}
	if inv != nil {
		var missing []string
		linked := map[string]bool{}
		for _, n := range t.Nodes {
			linked[strings.ToLower(n.Resource)] = true
		}
		for _, r := range inv {
			if !linked[strings.ToLower(r.ID())] && !linked[strings.ToLower(refOf(r))] {
				missing = append(missing, refOf(r))
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			b.WriteString("\nInventory resources not on the diagram: " + strings.Join(missing, ", ") + "\n")
		}
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// ----------------------------------------------------------------------------
// From the inventory

// refOf is how a resource is referenced in via: host/x, network/x…
func refOf(r *workspace.Resource) string {
	switch {
	case r.Kind == "Host":
		return "host/" + r.Name
	case workspace.IsNetworkKind(r.Kind):
		return "network/" + r.Name
	case r.Kind == "KubernetesCluster":
		return "cluster/" + r.Name
	case r.Kind == "GitRemote":
		return "git/" + r.Name
	}
	return strings.ToLower(r.Kind) + "/" + r.Name
}

func typeForResource(r *workspace.Resource) (string, string) {
	switch r.Kind {
	case "Host":
		return "server", ""
	case "HostGroup":
		return "", ""
	case "KubernetesCluster":
		return "k8s-cluster", ""
	case "Database":
		return "database", ""
	case "Panel":
		return "external", ""
	case "GitRemote":
		return "git", ""
	case "SocksProxy":
		return "forward-proxy", ""
	}
	if workspace.IsNetworkKind(r.Kind) {
		return "vpn-gateway", "remote access"
	}
	return "generic", ""
}

// ImportInventory adds a node for every inventory resource not yet on the
// diagram (linked by resource), and links for their via paths. Returns the
// IDs of the added nodes.
func ImportInventory(t *Topology, w *workspace.Workspace) []string {
	byRes := map[string]string{}
	for _, n := range t.Nodes {
		if n.Resource != "" {
			byRes[strings.ToLower(n.Resource)] = n.ID
		}
	}
	var added []string
	for _, r := range w.Resources {
		typ, variant := typeForResource(r)
		if typ == "" {
			continue
		}
		ref := refOf(r)
		if _, ok := byRes[strings.ToLower(ref)]; ok {
			continue
		}
		n := Node{ID: t.NewID(r.Name), Type: typ, Variant: variant, Label: r.Name, Resource: ref, Environment: r.Environment, Notes: r.Description}
		if a := r.Str("address"); a != "" {
			n.Addresses = []string{a}
		}
		switch r.Kind {
		case "Database":
			if e := r.Str("engine"); e != "" {
				n.Tech = []string{e}
			}
		case "GitRemote":
			n.Addresses = []string{r.Str("url")}
		default:
			if workspace.IsNetworkKind(r.Kind) {
				n.Tech = []string{r.Kind}
			}
		}
		t.Nodes = append(t.Nodes, n)
		byRes[strings.ToLower(ref)] = n.ID
		added = append(added, n.ID)
	}
	// via: "reached through"
	for _, r := range w.Resources {
		if r.Via == "" {
			continue
		}
		from, to := byRes[strings.ToLower(refOf(r))], byRes[strings.ToLower(r.Via)]
		if from == "" || to == "" {
			continue
		}
		exists := false
		for _, l := range t.Links {
			if (l.From == to && l.To == from) || (l.From == from && l.To == to) {
				exists = true
			}
		}
		if !exists {
			kind := "ssh"
			if hop := w.FindRef(r.Via); hop != nil && workspace.IsNetworkKind(hop.Kind) {
				kind = map[string]string{"WireGuard": "wireguard", "OpenVPN": "openvpn", "OpenConnect": "ssl-vpn", "OpenFortiVPN": "ssl-vpn"}[hop.Kind]
				if kind == "" {
					kind = "wan"
				}
			}
			t.Links = append(t.Links, Link{ID: t.newLinkID(to, from), From: to, To: from, Kind: kind, Label: "reaches " + r.Name, Encrypted: true})
		}
	}
	return added
}
