# Docker

The Docker page manages the engine of a workspace host over SSH with the `docker` CLI
that is already installed there — no daemon socket is exposed or forwarded. VPNs and jump
hosts work as everywhere else. When the SSH user is not in the `docker` group, opsdeck
uses `sudo -n docker` (it probes once per host; sudo must not ask for a password).

Pick a host at the top right (hosts with the role `docker` come first). The header shows
the engine version, the Compose plugin, whether the host is a swarm manager, and how many
containers run. The tab and what is open in it are part of the URL, so a compose project,
a volume or the event stream can be opened in its own workspace tab.

## Containers

- Grouped by compose project or swarm stack, with live CPU and memory per container
  (every 5 seconds), health, and the published ports in short form (`8080→80/tcp`).
- Start, stop, restart, pause, unpause, kill, remove.
- **Details** (click a name): state and health-check output, live usage, image and
  command, limits, ports, networks with their addresses, mounts (a volume opens in the
  volume browser), environment (values hidden until you show them) and labels;
  **Logs**; **Terminal** — `docker exec` with a real terminal (bash when the image has
  it, else sh; optional user); **Processes** (`docker top`); **Inspect** JSON.

## Logs

Container, compose-project and swarm-service logs share one viewer:

- **Live** with history (100 – 10 000 lines, or all) and a time window (last 5 minutes …
  last week); **Snapshot** loads the history without following.
- If the connection drops (network, VPN, laptop sleep), the stream **resumes from the last
  line it received** (`--since` that timestamp, without duplicates).
- ANSI colours are rendered as a terminal would; lines are tinted and marked by level
  (error, warn, debug — also from `level=…` and JSON `"level"` fields); stderr lines are
  marked and can be shown alone; every line has docker's timestamp (local time, full value
  on hover).
- Compose and service logs label each line with its service or task (`api-1`,
  `web.2@node`), each in its own colour; the chips above the lines hide or isolate them
  (shift-click).
- Filter by text or regular expression, show warnings and errors only, click a line to
  expand it (JSON is pretty-printed), copy a line, download what is shown.
- Large streams stay fast: the newest 10 000 lines are kept and only the visible ones are
  rendered.

## Compose

Every project `docker compose ls` knows on the host, with its status:

- **Up** (`up -d --remove-orphans`), **Pull**, **Restart**, recreate (`--force-recreate`),
  start, stop, **Down**, and **Down and delete volumes** (asks you to type the project
  name). Long actions keep running if you leave the page; their output opens in a dialog.
- **Services**: each container with state, health, exit code and ports; logs, terminal,
  and restart / stop / start per service.
- **Logs** of the whole project.
- **Files**: edit the compose files and the `.env` next to them. A change is checked with
  `docker compose config` (together with the project's other files) before it replaces the
  file; the old version stays next to it as `<file>.opsdeck-bak-<time>`, and owner and mode
  are kept. **Save & up** applies it right away.
- **New project**: a name, a directory (default `/opt/<name>`) and a `compose.yaml`
  (validated the same way), optionally started.

Compose v2 (the `docker compose` plugin) is required.

## Swarm

On a swarm manager:

- **Services** with replicas (running / desired), image and ports; scale, **redeploy**
  (`update --force`: replaces every task), change the image (rolling update), roll back,
  remove, and logs of all tasks. Expanding a service shows its tasks (node, state, error),
  refreshed every 3 seconds.
- **Stacks**: deploy or update from a compose file on the host (relative paths resolve
  next to it; remembered for the next update) or from pasted YAML, optionally with
  `--prune`; tasks; remove.
- **Nodes**: status, manager role, engine version; set availability to active, pause or
  drain.

## Volumes

Volumes with the compose project or stack they belong to and the containers that use them
(anonymous volumes are hidden unless you ask). Measure a volume's size, create or delete
volumes, and **browse their files**:

- navigate folders, view and **edit** text files, **download** any file, **upload** files
  (button or drag and drop, any size — streamed over SSH), create folders, delete files or
  folders;
- uploaded and edited files get the owner of their folder (so the container can read
  them) and replace an existing file atomically, keeping its mode and owner.

opsdeck reaches a volume's data, in order: directly on the host (when the SSH user can
read its directory), with `sudo -n` on the host, or through a short-lived helper container
that mounts the volume (`busybox` or `alpine` if present, else `busybox:stable` is pulled).
The badge next to the path says which one is used. Every operation runs a small script
that resolves the path and refuses anything outside the volume, also through symlinks.

## Images, networks, stats, events, system

- **Images**: pull (also "pull again" to update a tag), remove; dangling images are marked.
- **Networks**: driver, scope, owning project or stack; inspect (containers, subnets);
  remove (not the built-in ones).
- **Stats**: every running container, refreshed every 3 seconds, sortable by CPU or memory.
- **Events**: the engine's event stream, live — the last hour, then new events as they
  happen; filter by type (container, image, network, volume, service, node…) or text.
- **System**: engine details and warnings, disk usage (`docker system df`), and clean-up:
  stopped containers, dangling or unused images, build cache, unused networks, unused
  volumes (asks you to type the host name).

Every change is recorded in the audit log.

## The agent

The agent's `docker` tool has the same reach: containers (ps, inspect, logs with
`tail`/`since`, top, stats, start/stop/restart/pause/unpause/kill/rm), compose (ls, ps, logs,
read a compose file or `.env`, up, recreate, pull, restart, stop, start, `compose_write` —
validated, with a diff on the approval card and an optional `up -d` — down, down with
volumes), swarm (services, tasks, service logs, scale, redeploy, rollback, image update,
remove, stacks, stack deploy/remove, nodes, node availability), volumes (list, list and read
files, write, delete), images (pull, remove), networks, `df` and prune. Reads run without
asking; changes follow the approval mode; removals, `down`, prune and volume deletes are
destructive and always ask.
