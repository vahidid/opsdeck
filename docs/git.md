# Git repositories

Deployment files usually live in Git: Kubernetes manifests, Helm values, compose files,
Ansible, Terraform. A workspace connects those repositories (GitLab, self-hosted or
gitlab.com, and GitHub or GitHub Enterprise Server) as `GitRemote` resources. opsdeck keeps a
working copy of each in the workspace, and you and the agent read, change, commit, pull,
push and open and merge merge requests on it. A repository can also serve as
documentation.

## Connectors: connect your account once

**Connectors** (sidebar, under the workspace list) holds the GitHub and GitLab accounts
of this computer: GitHub (github.com), GitHub Enterprise Server, GitLab.com and any
self-hosted GitLab. They work like Claude Code's GitHub connection: connect once, then
every workspace picks repositories from the account. Three ways to sign in, offered when
available:

| Way | How |
|---|---|
| Your `gh` / `glab` login | when the GitHub CLI or GitLab CLI is already logged in to that server, one click copies its token |
| Sign in with the browser | OAuth device sign-in (RFC 8628): opsdeck shows a code, opens the provider's page, you approve, done. Needs an OAuth application: GitHub OAuth app with *Enable Device Flow*, or a GitLab application (not confidential, scopes `api read_user write_repository`, GitLab 17.2+); its ID goes under *Advanced*. A build can ship a default GitHub client ID (`-X opsdeck/internal/gitx.GitHubClientID=…`). GitLab tokens expire after two hours and are renewed automatically with the refresh token |
| Access token | *Create a token* opens the provider's page with the scopes already selected (GitHub classic: `repo read:user user:email workflow`; GitLab: `api read_user write_repository`); paste it |

The token is checked against the account (its login is shown) and stored in the vault
(global scope, `vault://global/git-<connector>`, usable by the agent, never shown to it).
For a self-hosted server with a private CA, paste the CA under *Advanced*. Reconnecting the
same server replaces the token; disconnecting deletes it.

## Adding a repository to a workspace

**Repositories → Add repository**: choose the account, search its repositories (most
recently active first; read-only ones are marked), pick one, optionally set the
environment and *Use as documentation*, and *Add and clone*. If the server is only reachable
through a VPN or bastion of the workspace, choose it in *Reach the server through*. It is
used to list the repositories and is saved as the repository's `via`.

The resource only holds the connector and the URL:

```yaml
- kind: GitRemote
  name: deploy
  environment: prod
  spec:
    connection: gitlab-example   # optional: by default the connector whose server serves the URL
    url: https://gitlab.example.com/ops/deploy.git
    docs: true
```

Every other field is optional and folded under *Advanced* in the form:

| Field | |
|---|---|
| `branch` | branch to clone (default: the server's default branch) |
| `docs`, `docsPaths` | use the repository's Markdown as documentation (default paths `README.md` and `docs/`) |
| `protectedBranches` | extra branches (globs like `release/*`) the agent may only push to with approval |
| `token` | a vault item to use instead of the connector (e.g. a project access token), or for a server without a connector |
| `deployKey` | vault `ssh-key` for `ssh://` / `git@host:` URLs |
| `provider`, `apiUrl` | only for servers without a connector when auto-detection fails |
| `caCert` | private CA, when not set on the connector |
| `via` | `network/<vpn>` or `host/<jump>` |

*Add by URL instead* adds any Git URL (also servers other than GitHub/GitLab: pull and
push work, merge requests do not). *Test connection* checks Git access (`ls-remote`) and
the API separately.

## Working copies

The working copy of `deploy` is `repos/deploy/` in the workspace folder. `repos/` is added
to the workspace's `.gitignore`. The folder is created on clone and is left in place when
the resource is removed; renaming the resource renames it.

opsdeck runs the system `git` (it must be installed) with an isolated configuration:

- `~/.gitconfig` and the system config are ignored (`GIT_CONFIG_GLOBAL=/dev/null`,
  `GIT_CONFIG_NOSYSTEM`). No prompts, no credential helpers, no hooks (`core.hooksPath`),
  no `ext::` transport, no fsmonitor.
- **Tokens** (the connector's or the repository's own) reach git only as an HTTP header in its environment, scoped to the remote's
  origin (`http.https://host/.extraHeader`), so a redirect to another host never gets it.
  They never appear on a command line, in `.git/config` or on disk.
- **Deploy keys** are served by an in-memory ssh-agent that holds only that key, on a socket
  in a private temporary folder that lives as long as the git command. Passphrase-protected
  keys are decrypted in memory. Windows writes a 0600 key file that lives for the same time,
  because its OpenSSH only talks to named-pipe agents.
  SSH host keys are pinned on first use in `.opsdeck/git_known_hosts`.
- **`via`** — opsdeck starts a SOCKS5 proxy on 127.0.0.1 for each git command, with a
  random password. It dials through the VPN or jump host. HTTPS uses it as `http.proxy`;
  SSH uses `opsdeck socks-connect` as `ProxyCommand`. Proxy variables from the environment
  are dropped, so traffic cannot leave by another route. Without `via`, the usual
  `HTTPS_PROXY` settings apply.
- One git operation at a time per repository.

Commits are authored by *App settings → Git commit name/email*, or else by the token's
owner (GitLab `/user`; GitHub's no-reply address). Commits by the agent carry a trailer
`Opsdeck-Agent-Session: <session id>`, so every change can be traced to its transcript.

## The Repositories page

- **List** — every repository with branch, ahead/behind, uncommitted changes, last commit,
  clone/pull, test connection, edit.
- **Files** — tree, viewer (Markdown preview), editor (Ctrl/⌘-S), new/delete file.
- **Changes** — changed files with their diff, select files, commit (optionally push
  right after), discard.
- **History** — commits with their patches.
- **Merge requests** — open/merged/closed lists with CI status and merge status, new merge
  request (proposed after pushing a branch), comment, merge (merge/squash/rebase, delete the
  source branch).
- Branch menu (switch, new branch from the current branch or `origin/<default>`), pull,
  pull with rebase, fetch, push / publish branch. Pushing to the default branch asks
  first.

Documentation repositories also show their Markdown under **Docs → From repositories**,
read-only, with a link to edit the file in the repository.

## The agent

| Tool | Actions | Approval |
|---|---|---|
| `repo` | `list`, `status`, `log`, `diff`, `show`, `branches` | never |
| | `clone`, `fetch`, `pull` (fast-forward) | never: they only update the local copy |
| | `checkout` (switch/create branch), `commit`, `pull` with rebase | asks; one "always" covers every edit of that repository in the session |
| | `push` | asks, showing the outgoing commits and diff; the default branch, branches protected on the server or listed in `protectedBranches`, and protected environments always ask (also in Auto mode) |
| | `push` with `force` (`--force-with-lease`), `discard` | always asks (destructive) |
| `repo_files` | `list`, `read`, `search` (also at any branch/commit) | never |
| `repo_write` | `write`, `edit` (exact replacement), `delete` | like `commit`; the diff is the preview |
| `merge_request` | `list`, `get` | never |
| | `create`, `comment` | asks |
| | `merge` | always asks: it changes the target branch, which often deploys |

The system prompt tells the agent the expected workflow: pull, create a branch
(`opsdeck/<topic>`), edit, check the diff, commit, push the branch, and open a merge
request that says what changes, why, how it was verified and how to roll back. It does not
push to the default or a protected branch, or merge, unless asked. Documentation
repositories are listed with the other docs (`repo/<name>/<path>`) and are read with
`read_doc`.

`git_sync` remains for deploying: it clones or updates a repository **on a host**
(e.g. `/opt/apps/shop`) with the same token, which is passed through stdin.

## API

See [api.md](api.md#git-repositories).

## Testing

- `internal/gitx/gitx_test.go` runs against a real Git smart-HTTP server (`git http-backend`
  behind token auth, `internal/testutil/gitserver`) and a fake GitLab API. It covers:
  - HTTPS with a private CA: the clone fails until `caCert` is set, then fails until the
    token is set;
  - docs, path containment (`..`, `.git`, symlinks), branch, edit, diff, commit identity
    and trailer, push, protected branches;
  - merge requests, fast-forward pull, diverged pull and rebase, discard;
  - that the token is never written to disk.
- `TestSSHDeployKey` clones and pushes over SSH with a passphrase-protected deploy key
  directly and through a jump host (`socks-connect`). `TestThroughVPN` does it over HTTP
  inside an in-process WireGuard network with the environment's proxy variables pointing
  nowhere.
- `TestConnector` signs in with the device flow against a fake GitLab, finds the
  connector of a repository that has only a URL, renews the expiring token on use and
  clones, commits and pushes with it. `internal/server/connectors_test.go` connects through
  the API with a token (a wrong one is refused), a `glab` login and the browser sign-in,
  lists the account's repositories from a workspace, adds and clones one, and checks that
  disconnecting deletes the token.
- `TestGitHubAPI` covers pull requests, checks and merge on GitHub's API.
- `internal/server/git_test.go` drives the agent with a scripted model through the
  entire flow, approvals included, and the REST API.
