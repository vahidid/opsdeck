# Kubernetes

The Kubernetes page works with the clusters of a workspace (`KubernetesCluster` resources
whose kubeconfig is in the vault). It talks to the API server directly with client-go —
no `kubectl` or `helm` binary is needed. A cluster with `via: host/bastion` or
`via: network/office-vpn` is reached through that host or VPN, also for logs, terminals and
port-forwards.

Pick a cluster and a namespace (or **All namespaces**) at the top right; the choice is
remembered per cluster. What is open — the view, an object and its tab — is part of the
URL, so it can be opened in its own workspace tab.

## Navigation

The left side lists **Overview**, **Events** and **Helm releases**, then every resource
type the API server offers, grouped into Workloads, Network, Config, Storage, Access,
Cluster, **Custom resources** (from installed CRDs) and Other. Search by kind, plural or
short name (`deploy`, `pvc`, `cm`…). After installing a CRD, the refresh button re-reads
the API's resource types.

## Overview

- Nodes ready, pods running / pending / failed, workloads, and the number of problems.
- **Problems**, each opening the object: crash-looping containers (also between
  back-offs), image pull errors, recent OOM kills, pods pending for over two minutes,
  failed pods, containers restarted five times or more, deployments / StatefulSets /
  DaemonSets with unavailable replicas, failed jobs, pending volume claims, NotReady nodes,
  pods stuck terminating.
- **Nodes** with CPU and memory in use (with metrics-server) and requested, pods per node,
  pressure conditions and cordon state.
- The last hour of **warnings**.

## Resource tables

Every type is shown with the columns the API server itself defines for it (the same ones
`kubectl get` prints), so custom resources get their printer columns too. **Wide** adds
the extra columns (node, IP, images, selectors…).

- Live: refreshed every 5 seconds; pods and nodes also show CPU and memory in use.
- Filter by text, or by a label selector (`app=web,tier!=db`) sent to the API.
- The line under a name shows its owner (`ReplicaSet/web-5d9b…`); status columns are
  coloured; deleting objects are dimmed.
- Row actions: logs, terminal (pods), rollout restart, run now (CronJobs), and in the menu
  cordon / uncordon, pause / resume a rollout, suspend / resume a CronJob, delete.

## Object details

Click a row. The drawer has:

- **Overview** by kind — pods: phase, node, IPs, QoS, volumes, and per container the
  image, state (with the last termination's reason and exit code), restarts, ports,
  command, mounts, and live CPU / memory against requests and limits; workloads: replica
  bars (ready, up to date, available), strategy, selector, images; services: type, cluster
  IP, load balancer, ports with target ports; ingresses: hosts, paths, backends, TLS;
  nodes: kubelet, OS, kernel, runtime, addresses, taints, usage against allocatable;
  CronJobs and Jobs: schedule, last run, completions; volumes and claims. Then conditions
  and metadata (owner, labels, annotations).
- **Pods** of a workload, service or node, live.
- **Logs** of a pod or of all pods of a workload (see below).
- **Terminal** (pods): `kubectl exec -it` with a real terminal — bash when the image has
  it, else sh; pick the container.
- **History** (Deployments, StatefulSets, DaemonSets): every revision with its images;
  **Roll back** to any of them.
- **Data**: ConfigMap values; Secret keys with their values hidden until you click
  **Reveal values** (recorded in the audit log).
- **Events** of the object.
- **YAML**: the live object; **Edit** opens it without status and server-set fields,
  **Review changes** shows the diff from a server-side dry run, **Apply** applies it.

Header actions depend on the kind: scale, restart, set image (per container), pause /
resume a rollout, run a CronJob now, suspend / resume it, cordon / uncordon, **drain** (the
node is cordoned and its pods evicted; DaemonSet and static pods stay; a
PodDisruptionBudget that blocks an eviction is reported), **port forward**, and delete
(type the name to confirm, except for pods).

## Logs

The same viewer as for Docker: live with history (100 – 10 000 lines per container, or
all) and a time window, ANSI colours, level tinting, text / regex filters, warnings and
errors only, expandable JSON, download. Specific to Kubernetes:

- A workload's logs merge **all its pods** (and containers), each labelled and coloured;
  pods that start later (a rollout, a scale-up) join the stream automatically.
- Pick one container or all, and **Previous** for the crashed instance's logs.
- Lines carry the kubelet's timestamps, so a dropped stream resumes from the last line
  received without duplicates.

## Events

The cluster's events live: the last hour, then each new or repeated event as it happens
(a repeated event updates its row and count). Filter by text or show warnings only; click
a row to open its object.

## Helm releases

Helm 3 releases read from the release secrets in the cluster: chart and app version,
revision, status, and for any revision its values, rendered manifest, notes and the
release history. Read-only — upgrades stay with `helm` or your GitOps tool.

## Apply

**Apply YAML** (top right) takes one or more manifests (pasted, or loaded from files;
`---` separated documents and `List`s both work) and a default namespace for objects
without one. **Review changes** asks the API server for a dry run and shows, per object,
whether it would be created, configured or left unchanged, with a diff; **Apply** applies
it. opsdeck uses server-side apply with the field manager `opsdeck` (taking over fields
from other managers), so a field you remove from the manifest is only removed if opsdeck
had set it. Secret values never appear in diffs.

## Port forwarding

From a pod, a service (a ready pod behind it, with the service's target port — named
ports work) or a workload: choose a port, and opsdeck listens on `127.0.0.1:<port>`. It is
listed with the other forwards in the top bar and stops when you stop it or lock the
vault.

## The agent

The agent's `kubernetes` tool reaches the same API, for any resource type including
custom resources:

| Action | Risk |
|---|---|
| `get` (with label / field selectors, wide), `describe` (object, its pods and events), `yaml`, `logs` (a pod or a workload's pods; container, tail, since, previous), `events`, `top` (pods / nodes), `overview` (problems, nodes, warnings), `resources`, `history`, `rollout_status`, `helm` (releases, values) | read |
| `apply` (the approval card shows the server-side dry-run diff), `restart`, `scale`, `set_image`, `pause`, `resume`, `undo`, `suspend`, `unsuspend`, `trigger`, `cordon`, `uncordon` | change |
| `exec` — a command in a container, classified like a shell command | per command |
| `delete`, `delete_pod`, `drain` | destructive (always asks) |

Secret values are masked for the agent as well (size and a short hash, so changes are
still visible). Every change, from the UI or the agent, is recorded in the audit log.
