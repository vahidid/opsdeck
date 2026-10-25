# Deployments

opsdeck reads and drives the deployment platforms a workspace uses — **Portainer**,
**Argo CD** and **Komodo** — through their APIs: what each one deploys, its health,
services, history and logs, what a deploy would change, and the actions (deploy / sync,
restart, rollback…). They are on the **Deployments** page and in the agent's `deploy`
tool. Every action is recorded in the activity journal.

## A platform is a resource of the workspace

Unlike the Git and DNS connectors (accounts connected once on this computer), a deployment
platform belongs to the workspace: it is usually a self-hosted server of that
infrastructure, one per environment or customer, often reachable only over a VPN. So it is
a `DeployPlatform` resource, with its own URL and its own credential in the workspace's
vault, and it is reached through its `via` like any other resource:

```yaml
- kind: DeployPlatform
  name: portainer
  environment: prod
  via: network/office-wg            # optional: the server is only reachable over the VPN
  spec:
    platform: portainer             # portainer | argocd | komodo
    url: https://portainer.acme.internal:9443
    token: vault://acme/portainer-token
    targets: [shop-*, billing]      # optional: only these stacks / applications belong here
    notes: |                        # optional, for the agent
      The shop runs as the compose stack shop-web; billing deploys from Git.
    caCert: |                       # optional: a private CA (or insecure: true for a test server)
      -----BEGIN CERTIFICATE-----
```

Add it on the Deployments page (*Add a platform*) or under Resources. Workspace files stay
shareable: they hold the URL and a `vault://` reference, never the token.

When one platform also serves other projects, `targets` (glob patterns on the names)
limits the workspace to its own stacks or applications: the others are not listed, and
neither the page nor the agent can act on them.

| Platform | Credential | Created at | Rights |
|---|---|---|---|
| Portainer (CE/BE 2.x) | an access token (vault item *API token*), sent as `X-API-Key` | My account → Access tokens | the token acts as its user: the stacks of the environments that user can reach; changing them needs an administrator or the stack's owner/team |
| Argo CD (v2, v3) | an account token (vault item *API token*), sent as `Authorization: Bearer` | Settings → Accounts, or `argocd account generate-token` for an account with the `apiKey` capability | its RBAC role: `applications, get` to read, `sync` and `action` to deploy and restart |
| Komodo (v1, v2) | an API key and secret (vault item *Login*: the key as username, the secret as password), sent as `X-Api-Key` / `X-Api-Secret` | Settings → Api Keys, or on a service user | the user's permissions: Read to list, Execute to deploy |

Redirects are never followed, so the credential never goes to another server.

## What a platform deploys

Each platform's objects come in one shape, a **target**:

| | Portainer | Argo CD | Komodo |
|---|---|---|---|
| target | a stack (compose, swarm, kubernetes) | an application | a stack or a deployment |
| id | the stack id | `namespace/name` | `stack:<id>`, `deployment:<id>` |
| where | the environment | the cluster and namespace (and project) | the server |
| status | active, inactive (deploying, error on recent versions) | the health (Healthy, Progressing, Degraded…) and the sync status (Synced, OutOfSync) | the state (running, stopped, unhealthy…) and image updates |
| services | the containers, through Portainer's Docker proxy (unhealthy or stopped ones mark the stack) | the managed resources with sync status and health (from the resource tree on v3) | the compose services / the container, with available updates |
| history | — (Portainer keeps none; the activity journal has the deploys made from opsdeck) | the sync history (revision, time, who) | the updates (operation, time, user, result) |
| diff | — | each resource's live state against the state after a sync | the deployed compose files against the latest (the file in Komodo, or the repository) |
| logs | the containers' logs | the pods of the Deployments/StatefulSets/DaemonSets | `docker compose logs` / the container's log |

## Actions

| Action | Portainer | Argo CD | Komodo | Risk |
|---|---|---|---|---|
| `deploy` | redeploy: a Git stack pulls its repository (the saved Git credentials are kept), a file stack redeploys its file; `pull` pulls the images first | sync to the target revision, or `revision` (a branch, tag or commit) for this sync; `prune` also deletes what is no longer in Git | deploy (`docker compose up` / recreate the container); `service` for one service | change; with `prune` destructive |
| `update` | replace the compose file (not for Git stacks) and redeploy; the stack's environment variables are kept | — | — | change |
| `rollback` | — | to a history id; refused while auto-sync is on (revert in Git instead) | — | change |
| `refresh` | — | compare with Git again (`hard` clears the manifest cache) | — | read |
| `restart` | restart the containers (`service`: one) | rollout restart of the workloads (`service`: one) | restart | change |
| `start`, `stop` | start / stop the stack | — | start / stop | change |
| `pull` | — | — | pull the images | change |
| `terminate` | — | stop the running sync | — | change |

Komodo runs an action in the background: opsdeck follows it until it completes and shows
its log (up to ten minutes; after that it says it is still running). Argo CD's sync and
rollback start an operation the platform carries on; the page refreshes while a target is
busy.

## The Deployments page

The page lists the targets of the chosen platform — name and source, type, where it runs,
status with health and sync, revision, when it was last deployed — with a filter, a count
of targets that need attention (unhealthy, degraded, out of sync), and a *Deploy* button on
each row. A click opens the target: services or resources with their health, the running or
last operation, the history (Argo CD: *Roll back* on earlier entries), **Logs**, **Diff**
(what a deploy would change) and, for stacks, the **Stack file** (editable for Portainer
file stacks: *Save and redeploy* shows the change before it is sent).

Every action opens a confirmation with its options (revision, prune, pull, one service) and,
for a deploy, what changes. On a platform in a **protected** environment, and for any
prune, the target's name must be typed.

## The agent's `deploy` tool

The tool is offered when the workspace has a `DeployPlatform`. It uses the platform's vault
credential (the agent never sees it) and is classified like every other tool:

| Action | | Risk |
|---|---|---|
| `platforms` | every platform with its version, the signed-in user, the number of targets and how many need attention, its notes | read |
| `targets` | what a platform deploys, one line each | read |
| `status` | one target: services/resources, operation, history, the actions it takes | read |
| `logs`, `diff`, `file` | recent logs (`service`, `tail`), what a deploy would change, the stack file | read |
| `deploy`, `update`, `rollback`, `restart`, `start`, `stop`, `pull`, `terminate`, `refresh` | as above | change (`deploy`/`rollback`/`update` with `prune`: destructive; `refresh`: read) |

Before a change, the tool looks the target up and checks the action (refusing, for example,
a rollback under auto-sync, an `update` of a Git stack, an unknown service, or a target
outside `targets`), so the approval card shows the target's current state and what changes:
Argo CD's live-vs-Git diff or Komodo's deployed-vs-latest files for a deploy, the file diff
for an update, the history entry for a rollback. After the action it reads the target again
and reports its new state. Permission rules match the action and the target
(`deploy shop`, `restart shop worker`, `deploy shop prune`) on `deploy/<platform>`.

The system prompt tells the agent to look before it acts (targets, status, logs, diff),
deploy one target at a time and check it is healthy afterwards, not to bypass the platform
with `docker` or `kubectl` on what it manages, and to change Git-driven targets through
their repository (`repo_write`, `merge_request`) before deploying.

## API

See [api.md](api.md#deployments): `/api/deploy/platforms` and
`/api/workspaces/{ws}/deploy` (platforms, targets, target, logs, diff, action).

## Code

- `internal/deployx` — the provider-neutral model (`Target`, `Detail`, `Service`,
  `Deployment`, `Action`), the `Platform` interface and its implementations
  (`portainer.go`, `argocd.go`, `komodo.go`), and the `Manager` that opens a
  `DeployPlatform` resource: credential from the workspace's vault (agent use checked),
  TLS settings, dialed through the resource's via chain, limited to `spec.targets`.
- `internal/server/deploy.go` — the routes; actions are audited as `deploy.<action>` on
  `deploy/<platform>/<target>`.
- `internal/agent/deploytool.go` — the `deploy` tool.
- `internal/testutil/deployserver` — fakes of the three APIs for tests:
  `internal/deployx/deployx_test.go` (each platform's calls and shapes, errors, scope),
  `internal/agent/deploytool_test.go` (actions, risks, previews, no credential in outputs),
  `internal/server/deploy_test.go` (the routes and the audit).
