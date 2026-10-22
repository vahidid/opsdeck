# Git repositories

Deployment files usually live in Git: Kubernetes manifests, Helm values, compose files,
Ansible, Terraform. A workspace connects those repositories (GitLab, self-hosted or
gitlab.com, and GitHub or GitHub Enterprise Server) as `GitRemote` resources. opsdeck keeps a
working copy of each in the workspace, and you and the agent read, change, commit, pull,
push and open and merge merge requests on it. A repository can also serve as
documentation.

## Connecting a repository

On **Repositories → Connect repository** (or Resources → New → Git repository):

| Field | |
|---|---|
| `url` | `https://gitlab.example.com/group/project(.git)`, `ssh://git@host:2222/group/project.git` or `git@host:group/project.git` |
| `branch` | branch to clone (default: the server's default branch) |
| `provider` | `auto` (github.com → GitHub, hosts named *gitlab* → GitLab, otherwise the server is asked: `/api/v4/version`, `/api/v3/meta`), `gitlab` or `github` |
| `token` | vault item (`token`, or `login` with username + password/token). Used for HTTPS clone/pull/push and for the API |
| `deployKey` | vault `ssh-key` for SSH URLs (merge requests still need a token) |
| `apiUrl` | only when the API is somewhere unusual (default `https://host/api/v4`, `https://api.github.com`, `https://host/api/v3`) |
| `caCert` | PEM of a private CA (self-hosted GitLab with an internal certificate) |
| `protectedBranches` | extra branches (globs like `release/*`) the agent may only push to with approval |
| `docs`, `docsPaths` | use the repository's Markdown as documentation (default paths `README.md` and `docs/`) |
| `via` | `network/<vpn>` or `host/<jump>` when the server is only reachable through a VPN or a bastion |

Token scopes: GitLab project/personal/group access token with `api` and
`write_repository` (Developer role or higher to push). GitHub fine-grained token with
*Contents* and *Pull requests* read/write (classic: `repo`).

*Test connection* checks Git access (`ls-remote`) and the API (repository info, push
permission) separately.

## Working copies

The working copy of `deploy` is `repos/deploy/` in the workspace folder. `repos/` is added
to the workspace's `.gitignore`. The folder is created on clone and is left in place when
the resource is removed; renaming the resource renames it.

opsdeck runs the system `git` (it must be installed) with an isolated configuration:

- `~/.gitconfig` and the system config are ignored (`GIT_CONFIG_GLOBAL=/dev/null`,
  `GIT_CONFIG_NOSYSTEM`). No prompts, no credential helpers, no hooks (`core.hooksPath`),
  no `ext::` transport, no fsmonitor.
- **Tokens** reach git only as an HTTP header in its environment, scoped to the remote's
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
- `TestGitHubAPI` covers pull requests, checks and merge on GitHub's API.
- `internal/server/git_test.go` drives the agent with a scripted model through the
  entire flow, approvals included, and the REST API.
