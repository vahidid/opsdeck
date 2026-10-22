# Scripts

A script is a reusable shell script (server bootstrap, hardening, installing an agent,
rotating logs…) that opsdeck runs on hosts **as one unit**: the exact content you
reviewed, with typed parameters and secrets taken from the vault. You keep the
procedures you trust here; the agent runs them instead of redoing every step by hand,
and can write new ones for you to review.

> "I added a new server" → the agent sees `server-bootstrap` (meant for `new-host`),
> checks the system, fills its parameters and runs it with `run_script` — one approval,
> one unit, the full output saved.

Scripts and [skills](skills.md) work together: a skill is knowledge the agent reads and
follows with judgment; a script is code that runs as written. A skill can say "run the
`server-bootstrap` script, then verify with `net_check`".

## Where scripts live

| Scope | Folder | For |
|---|---|---|
| **My library** | `<opsdeck data>/scripts/` (next to the vault) | your own toolkit, available in **every** workspace |
| **This workspace** | `<workspace>/scripts/` | scripts of one project, committed with the workspace and **shared with the team** |

A workspace script with the same name replaces your own one in that workspace. On the
Scripts page, **Pin to workspace** copies one of your scripts (at its current version)
into the workspace so teammates get exactly what you run; **Save to my library** does
the opposite.

## On disk

```
scripts/server-bootstrap/
├── script.yaml
├── script.sh            # any interpreter, from its #! line (default bash)
└── versions/            # earlier versions: v1.yaml + v1-script.sh, …
```

```yaml
name: server-bootstrap
description: Initial setup of a fresh Ubuntu/Debian server — deploy user, SSH hardening, ufw, docker
version: 3
os: [ubuntu-22.04, ubuntu-24.04, debian-12]  # ID[-VERSION_ID] from /etc/os-release; "ubuntu" = any version
when: [new-host]                             # hints for the agent
sudo: true                                   # runs as root with sudo -n (needs NOPASSWD)
risk: change                                 # or destructive: always asks
timeoutSec: 1800
check: test -f /etc/opsdeck/bootstrap-v3     # exits 0 → already applied → the host is skipped
params:
  - { name: DEPLOY_USER, default: deploy }
  - { name: SSH_PORT, type: int, default: "22" }
  - { name: AUTHORIZED_KEY, type: secret, required: true }  # a vault:// reference when run
createdBy: user
updatedBy: agent
```

Parameters reach the script as **environment variables of the same name**, together with
`OPSDECK_HOST`, `OPSDECK_WORKSPACE`, `OPSDECK_SCRIPT` and `OPSDECK_SCRIPT_VERSION`. Types
are `string` (default), `int`, `bool` (`true`/`false`) and `secret`. A secret's value is
a `vault://scope/item` reference (optionally `#field`, e.g. `#public_key`; without a field
the item's secret value is used) — a literal value is refused, also as a default. Write
scripts to be idempotent: safe to run twice on the same host.

## Approval and versions

A script is **approved** while the hash you approved matches its content. The hash covers
everything that decides what runs: the script, the check, `sudo`, the supported systems
and the parameters with their defaults (not the description or hints).

Approvals are kept **on your computer**, never in the shared files: for a workspace in
`.opsdeck/script-approvals.json` (git-ignored), for your library in its folder. Anyone who
can change a script could also write its hash into it, so each person approves the
workspace's scripts themselves — a pinned script is approved for you and a draft for a
teammate until they review it.

- What you save yourself (editor or upload) is approved as saved.
- What the agent saves (`script` tool) is a **draft** as soon as what runs changes. So is a
  script changed outside opsdeck (a hand edit, a teammate's commit) — review it again.
- Every change of what runs creates a new version; the previous one is kept under
  `versions/`. The page shows a draft's changes against the previous version and an
  **Approve** button; approving sends the hash you looked at, so a change made in between
  is never approved by accident.

| | the agent runs it with `run_script` |
|---|---|
| approved, `risk: change` | like any change: asks in Ask mode, runs in Auto mode outside protected environments; "always allow" covers this script version with these parameters on these hosts, until the script changes |
| approved, `risk: destructive` | always asks |
| draft | always asks (shown as destructive, "not reviewed"); **refused** on protected environments |

The agent's file tools cannot reach your library, its local writes (including to a
workspace's `scripts/` or `.opsdeck/`) always ask, and saving through the `script` tool
always goes through the draft rule above.

## Running

From the **Run** tab (pick hosts or groups, fill parameters, secrets from the vault) or by
the agent with `run_script`. For each host opsdeck:

1. sends the script, the parameters and the secret values over the SSH session's
   **stdin** — never on the command line, where `ps` would show them;
2. writes them into a private temporary folder (`mktemp -d`, `umask 077`), removed
   afterwards also when the run is interrupted;
3. stops with exit code 97 if the host's system is not in `os`;
4. runs `check` (when set, unless forced) and skips the host when it passes;
5. runs the script with its interpreter (as root with `sudo -n` when `sudo: true`), so it
   works where `/tmp` is mounted `noexec`.

Up to 8 hosts run in parallel. Output is redacted (vault secrets, common credential
patterns) before it is shown, sent to a model or saved. Each host's full output is saved as
an artifact (`artifacts/<date>/<host>/script-<name>-v<N>.log`), and every run is recorded
in the script's **History** (`.opsdeck/script-runs.jsonl`, without secret values). A run
started from the page keeps going if the page is closed.

## Agent tools

| Tool | |
|---|---|
| `script` — list, read (also an earlier version), save | read / change (saved as a draft) |
| `run_script(script, target, params, force)` | change for approved scripts (destructive if the script says so); destructive for drafts, refused on protected environments |

The system prompt lists every script with its scope, status, `when` hints, systems and
parameters, and asks the agent to prefer a fitting script over redoing its steps and to
offer to save procedures worth repeating.
