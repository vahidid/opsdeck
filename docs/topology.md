# Topology

The **Topology** page of a workspace is an interactive map of its infrastructure: what sits
where, which product or technology each part is, what runs on it, and how the parts talk
to each other. It is saved as `topology.yaml` in the workspace folder, so it is shared
like the rest of the workspace. The agent reads it in every session and keeps it up to
date with you.

## What can be on it

- **Zones**: data center, region, cloud account, VPC/VNet, subnet, VLAN, DMZ,
  office/site, cluster, namespace, environment. Zones nest (a DMZ inside a data center)
  and carry a CIDR, a provider (suggested: ArvanCloud, AWS, Google Cloud, Azure, Hetzner,
  DigitalOcean, OVHcloud, Oracle, Alibaba, Iran Server, ParsPack, Asiatech, Afranet,
  on-prem…) and notes. Drag a component or a zone into a zone to move
  it there; drag a selected zone's corners to resize it.
- **Components**: about 45 types in six groups, each with kinds (variants) and suggested
  products:

  | Group | Types |
  |---|---|
  | Edge & access | internet, ISP/uplink, users/clients, office/branch, DNS, CDN, WAF, DDoS protection, load balancer (L4, L7, GSLB, cloud, keepalived VIP), reverse proxy, API gateway |
  | Network | router (edge, core, distribution, border/BGP, branch, wireless, virtual/cloud, LTE/5G), switch (L2 access, L3, core, ToR…), firewall (perimeter, NGFW, segmentation, host, cloud security group), VPN gateway (site-to-site, remote access, mesh), NAT gateway, Wi-Fi, bastion, forward proxy |
  | Compute | server, VM, hypervisor, container host, Kubernetes cluster, serverless |
  | Data | database (primary, replica, cluster), cache, queue/stream, object storage, file/block storage, search |
  | Platform & DevOps | Git hosting, CI/CD, registry, monitoring, logging, tracing, alerting, secrets, identity/SSO, backup, mail |
  | External | cloud provider (ArvanCloud/ابرآروان, AWS, Google Cloud, Azure, Hetzner, DigitalOcean, OVHcloud, Iran Server, ParsPack, Asiatech…), external service, other |

  Each component has a name, kind, product/vendor (e.g. *MikroTik CCR2004*, *FortiGate
  100F*, *Cloudflare*), technologies (*RouterOS 7, BGP*, *nginx 1.25*), addresses, an
  environment, a link to an inventory resource (`host/app-1`, `network/office`…), tags and
  notes. It also lists its **deployments**: what runs on it (Docker Compose, container,
  Kubernetes workload, Helm release, systemd service, package, binary, static site, VM,
  serverless, cron), each with image or version, ports, replicas, the repository its
  deployment files come from (`git/<name>`) and its path on the host.
- **Connections**: drag from one component's handle to another. Kinds:
  - network: LAN, VLAN trunk, WAN, internet, fiber, MPLS, wireless, dedicated cloud link, peering;
  - tunnels: IPsec, WireGuard, OpenVPN, SSL VPN, GRE, VXLAN;
  - routing: BGP, OSPF, static route;
  - application: HTTP(S), gRPC, WebSocket, TCP/UDP, DNS, SMTP;
  - data: SQL, Redis, AMQP, Kafka, NFS/SMB, S3, replication, backup;
  - management: SSH, RDP, SNMP, syslog, metrics scrape, deploys-to.

  Each has a port, encryption, direction, bandwidth, VLAN and notes, and is drawn with
  the color and line style of its group.

Reading a busy map:

- Connections leave from the side of a component that faces the other end and run as
  right-angled lines (or straight, see the legend). Several connections between the same
  two components are drawn side by side.
- **Hover or select a component**: its connections and neighbours stay bright with their
  labels (`https:443 🔒`, `sql:5432`…) and everything else fades. Selecting a connection shows
  only it and its two ends. The inspector lists the component's connections; clicking one
  selects it.
- The **Connections** legend (top right) gives the color and line style of each category
  (network, tunnels/VPN, routing, application, data, management) with counts. Click a
  category to hide or show it. Labels can be *auto* (only highlighted connections), *all*
  or *none*.

Other controls:

- **Import inventory** adds the workspace's hosts, VPNs, clusters, databases… that are not on
  the map yet, linked to their resources, with their `via` paths as connections.
- **Auto-arrange** lays everything out again.
- **Export** downloads a Markdown description for any AI (with the YAML attached) or the raw
  `topology.yaml`.

The panel on the right shows exactly what the agent reads.

## The file

```yaml
version: 1
zones:
  - {id: dc1, label: Tehran DC, kind: datacenter, provider: on-prem}
  - {id: dmz, label: DMZ, kind: dmz, parent: dc1, cidr: 10.0.1.0/24}
nodes:
  - id: edge
    type: router
    variant: border (BGP)
    label: Edge router
    zone: dc1
    vendor: MikroTik CCR2004
    tech: [RouterOS 7, BGP]
    addresses: [203.0.113.1]
  - id: app-1
    type: server
    zone: dmz
    resource: host/app-1
    deployments:
      - {name: shop-api, kind: docker-compose, image: ghcr.io/acme/shop:1.4, ports: ["8080"], repo: git/deploy, path: /opt/shop}
links:
  - {id: l1, from: edge, to: app-1, kind: https, port: "443", encrypted: true}
layout: {}   # positions on the canvas only
```

Saving checks IDs and references (zones, parents without cycles, link ends). A save
fails if the file changed since it was read (`rev`). The page follows changes the agent
makes while you have nothing unsaved, and otherwise asks whether to load theirs or keep
yours.

## The agent

The system prompt carries a compact description of the topology: zones as a tree, each
component with product, technologies, addresses, inventory link and deployments, then
every connection with protocol, port and encryption. The agent is told to use it to know
where which tool or technology runs, and when it learns something the map lacks or
contradicts (a new server, a WAF in front of something, a deployment, a version), to tell
you and propose the change.

The `topology` tool:

- `get` (text or YAML) and `catalog` are reads.
- `add_node`, `update_node` (merges fields), `remove_node`, `add_zone`, `update_zone`,
  `remove_zone`, `connect`, `update_link`, `disconnect`, `import_inventory` and `replace`
  (a whole YAML the agent built as its own artifact) change the map. `batch` combines
  several changes under one approval.
- Every change asks you first, also in Auto mode, with the list of changes and a diff of the
  description; "always" covers further changes in that session. The change is validated
  before it is offered and again before it is saved.

## Testing

`internal/topology` tests validation, save/load with revisions, rendering, zone
removal and inventory import. `internal/server/topology_test.go` has a scripted agent
batch-edit the map: one approval in Auto mode, then "always". An invalid connection is
refused. The test also checks the export and the system prompt. With Claude Code as the model, a
Persian message about a new WAF made the agent check what it could, notice that the
map lacked it, ask, and after "yes" add the node and both connections in one approved
change.
