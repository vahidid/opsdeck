# Ansible

An Ansible project of a workspace is a folder of playbooks, inventories, variables and
roles: a new or existing folder in the workspace, a folder anywhere on this computer, or a
folder of one of the workspace's [Git repositories](git.md). The **Ansible** page edits its
files, runs playbooks with the output streamed live, keeps every run with its log, shows
inventories, and opens, edits and creates **Ansible Vault** files with the vault password.
The agent has the same abilities through three tools; when it needs a vault password, the
user types it into the approval card.

ansible itself runs on this computer (`pipx install --include-deps ansible`, or
`pip install ansible-core`; a virtualenv can be set per project). Vault files are read and
written by opsdeck itself, so viewing, editing and encrypting work without ansible.

## The project

```yaml
- kind: AnsibleProject
  name: infra
  environment: prod          # protected environments make the agent ask before runs
  via: network/office-vpn    # optional: how the hosts of the project's own inventory are reached
  spec:
    source: repo             # repo | folder
    repo: deploy             # the GitRemote (source: repo)
    path: ansible            # repo: folder inside it; folder: relative to the workspace
                             # (default ansible/<name>), or an absolute path / ~/…
    inventory: inventories/prod   # default inventory; "workspace" = generated from the workspace's hosts
    vaultPassword: vault://acme/ansible-infra       # optional saved password (vault id "default")
    vaultPasswords: [prod=vault://acme/ansible-prod]  # optional, other vault ids
    sshKey: vault://acme/deploy-key                 # optional key for the project's own inventory
    bin: ~/.venvs/ansible/bin                       # optional folder holding ansible-playbook
```

**Add project** on the page offers *New folder* (creates `ansible.cfg`, `inventory/hosts.yml`,
`site.yml` and `requirements.yml`), *Existing folder* and *Git repository*. In a repository
project the files are those of the working copy; commit, push and open a merge request on
the Repositories page (or let the agent do it).

## Files

The file tree marks what each file is: playbooks (a YAML list of plays), inventories
(`inventory/`, `inventories/<env>/`, `hosts*`), variables (`group_vars/`, `host_vars/`,
`vars/`, `defaults/`), roles, templates, `ansible.cfg` and requirements. Vault-encrypted files
and files with encrypted values carry a lock. Filters (playbooks, inventory, vars, vault),
search in files, new file (empty, playbook or variables template, optionally encrypted),
rename and delete. A playbook has a **Run** button.

## Ansible Vault

opsdeck implements the vault format itself (`$ANSIBLE_VAULT;1.1;AES256` and `1.2` with a
vault id: PBKDF2-SHA256, AES-256-CTR, HMAC-SHA256), so files stay compatible with
`ansible-vault` both ways.

| On the page | What it does |
|---|---|
| **View** | decrypts the file in this window only |
| **Edit** | edits the decrypted text and saves it encrypted again with the file's vault id |
| **Variable** / **Secret** | sets one variable: inside a vault file, or as an inline `!vault` value in a plain YAML file; *Generate* makes a random password |
| **Show values** | a YAML file with inline `!vault` values, shown with the values decrypted (read-only) |
| **Encrypt** | encrypts a plain file in place, with a vault id |
| **Change password** | re-encrypts one file with a new password (rekey) |
| **Decrypt file** | stores it as plain text (asks first: its secrets become readable on disk and in Git) |
| **Encrypt a value** | `encrypt_string`: a `!vault |` block to paste anywhere |

### Passwords

When an action needs a vault password that is not at hand, the page asks for it with the
vault id the file names. The password is then:

- used for that action only, or
- **kept in memory** until opsdeck locks (default), or
- also **saved in the vault** of the workspace (a `login` item `ansible-<project>`, referenced
  from the project as `vaultPassword` / `vaultPasswords`); you choose whether the agent may use
  it for runs — it never sees it.

The vault ids of the project are shown next to the tabs: green when a password is at hand
(click a remembered one to forget it), yellow when locked (click to unlock). Locking opsdeck
forgets every remembered password and stops the running runs.

A password reaches ansible only as a `--vault-id <id>@<file>` file in a private temporary
folder (`0700`, file `0600`) that is removed when the run ends; it is never on a command line,
in the run's history or in the log (the passwords in memory are masked in the output like vault
secrets). A typed password is checked against the project's encrypted content before it is
used, so a typo is reported at once.

## Running

The **Run** tab: playbook, inventory, limit, tags and skip tags, extra variables (YAML or
JSON, passed as a file), check (dry run), diff, verbosity, forks, start at task. Also
**Ad-hoc** (one module on a host pattern, with become), **Galaxy** (`ansible-galaxy install -r`)
and **Lint** (`ansible-lint`, when installed). *Syntax*, *Hosts*, *Tasks* and *Tags* run the
quick checks that do not touch hosts.

The output streams live with ansible's colours, timestamps, filters and search, and resumes
after a dropped connection; the run keeps going when the page is closed. When it ends, the
**PLAY RECAP** is shown per host (ok, changed, unreachable, failed, skipped…) and the status is
`ok`, `failed` (a task failed), `unreachable`, `error` (ansible itself, e.g. a syntax error)
or `canceled`. **Stop** interrupts ansible (SIGINT, then the whole process group).

**History** lists every run of the project — started on the page or by the agent — with its
recap and full log (`<workspace>/.opsdeck/ansible/`: `runs.jsonl` and `logs/<id>.log`, redacted,
local only), downloadable, and *Run again* copies its options.

### Reaching hosts

- **`inventory: workspace`** — an inventory generated from the workspace's hosts: each `Host`
  with `ansible_host`, port and user, grouped by host groups, roles and environments. Every
  host is reached the way opsdeck reaches it — through its VPN and jump hosts (a SOCKS proxy on
  127.0.0.1 with a random password, used by ssh as `ProxyCommand`) — with the keys of the vault
  served by an in-memory ssh-agent. Host keys pinned by opsdeck are trusted; new ones are
  pinned on first use in `.opsdeck/ansible_known_hosts`.
- **The project's own inventory** — reached directly with your ssh settings, or through the
  project's `via` (the same proxy, set with `ANSIBLE_SSH_COMMON_ARGS`); `sshKey` is served by an
  in-memory ssh-agent.

### Inventory

The **Inventory** tab shows an inventory's groups as a tree and its hosts with their groups
(`ansible-inventory`); clicking a host shows its variables, vault values decrypted.

## Agent tools

| Tool | Actions | Approval |
|---|---|---|
| `ansible` | `projects`, `files`, `read`, `search`, `inventory` (graph, without decrypted values), `syntax`, `list_hosts`, `list_tasks`, `list_tags`, `runs`, `log` | never (a folder outside the workspace asks once, like other local folders) |
| | `write`, `edit`, `delete` | like other local edits: one "always" covers the project (a repository project shares the repository's approval with `repo_write`); delete outside Git is destructive |
| `ansible_run` | `playbook`, `adhoc`, `galaxy`, `lint` | a real run is a change (protected environments always ask); a check run and read-only ad-hoc modules (ping, setup, read-only shell commands) ask once as reads and are allowed in Plan mode; the card shows the command, the hosts and the extra variables |
| `ansible_vault` | `view` | always asks: the decrypted content is shown to the model ("always" covers the project for the session) |
| | `edit`, `set` (with `generate`), `encrypt`, `encrypt_string` | changes like `write`; `decrypt` is destructive |

**The password prompt.** When an action needs a vault password that is not at hand, its
approval card has a password field — in every mode, also Auto. *Unlock once* uses it for this
action; *Unlock for session* also keeps it in memory until opsdeck locks (and allows the same
action again). A run asks when the project has vault content and no password at all is at hand;
for a project with several vault ids the agent names the one a run needs (`vault_id`). A wrong password keeps the card open with an error. The password goes from the
card to opsdeck only: it is not in the transcript, the audit journal (which records that a
password was entered) or anything the model receives. Saved passwords the item's policy allows
the agent to use, and remembered ones, are used without asking.

What the agent sees of decrypted content passes through the usual redaction: values of
variables named like credentials (`*password*`, `*token*`, `*secret*`, keys…) and known vault
secrets are masked. To change such a value it uses `set` instead of `edit`; `generate=true`
creates new passwords it never sees.

The system prompt lists the projects and asks the agent to look before it runs (files,
inventory, list_hosts), to do a check run with diff before changes that matter, to start small
with `limit` on protected environments, to report the PLAY RECAP, to follow the Git workflow
in repository projects, and never to ask for vault passwords in chat.

## API

See [api.md](api.md#ansible).

## Testing

- `internal/ansible/ansible_test.go`: decrypts files made by `ansible-vault` (1.1 and 1.2 with a
  vault id) and has `ansible-vault` open ours; inline values, `encrypt_string`, setting
  variables; file classification, path containment and symlinks; vault operations with
  passwords given once and remembered; runs of real playbooks with a vault file (live log,
  recap, history read back, check mode, a failing task, cancel, ad-hoc, quick checks) —
  skipped without ansible.
- `internal/server/ansible_test.go`: the page's API end to end (create with a starter layout,
  encrypt asking for the password, view with a wrong/right password, the workspace inventory)
  and a playbook run over SSH through a jump host with keys from the vault; then the agent:
  `ansible_vault view` with a wrong and a right password on the card, `ansible_run` with the
  password kept for the session, `set` with a generated value — and that the password is
  nowhere on disk.
