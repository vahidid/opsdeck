# Vault

The vault stores everything secret: SSH keys, usernames/passwords, API tokens,
kubeconfigs, VPN configs, certificates, secret files. It is local, encrypted at rest,
and unlocked only inside the daemon's memory.

## Threat model

Protects against:
- a stolen laptop, disk or backup (vault file is useless without the master password);
- other OS users reading our files;
- secrets leaking into workspace files, git, shell history, or AI model context;
- one project's credentials being used for another project by mistake.

Does **not** protect against:
- malware running as the same user while the vault is unlocked (no local password
  manager can). We reduce the window with auto-lock and "confirm on use".

## Key hierarchy

```
master password ──Argon2id(salt, params)──► KEK  (never stored)
                                             │ wraps
                                             ▼
                                    Vault Key (VK, random 256-bit)
                                             │ wraps
                        ┌────────────────────┼────────────────────┐
                        ▼                    ▼                    ▼
                 Scope key: global   Scope key: ws/acme   Scope key: ws/shop …
                        │                    │
                 encrypts items       encrypts items
```

- **Argon2id** for the password (default 64 MiB, 3 iterations, 4 lanes; parameters stored
  in the header so they can be raised later without breaking old vaults).
- **XChaCha20-Poly1305** for all encryption (random 24-byte nonces are safe).
- Each item's ciphertext is bound to its identity with associated data
  (`vault_id | scope_id | item_id | revision`), so ciphertexts cannot be swapped
  between items or scopes.
- **Why a separate Vault Key:** changing the master password only re-wraps VK; adding
  other unlock methods (OS keychain "remember on this device", YubiKey/FIDO2 later)
  is just another wrapping of the same VK.
- **Why a key per scope (workspace):** crypto-level isolation between projects, and
  later a single workspace can be exported/shared with a teammate without exposing
  anything else.

## Storage

SQLite file `vault.db`:

| table | plaintext columns | encrypted |
|---|---|---|
| `meta` | vault id, format version, KDF params, salt | wrapped VK (per unlock method) |
| `scopes` | scope id | wrapped scope key, scope name |
| `items` | item id, scope id, revision, timestamps | **everything else** (name, type, fields, tags, notes) |

Item names are encrypted too — someone with the file cannot see "prod-db-root". On unlock
the daemon decrypts the index into memory (fine for tens of thousands of items).

Every change bumps `revision` and keeps the previous revision for history/undo
(configurable retention).

## Scopes

- **global** — personal things used across projects (your own SSH key, GitLab token).
- **workspace scope** — one per workspace; created/deleted with the workspace.

A workspace can *reference* a global item, but must do it explicitly. A workspace can
never read another workspace's scope.

## Item types

All items share: `name`, `tags`, `notes`, custom fields, and a per-field `secret` flag.

| type | fields |
|---|---|
| `ssh-key` | private key, public key (derived), passphrase (optional), certificate (optional), comment |
| `login` | username, password, url(s), TOTP seed (optional) |
| `token` | value, header/scheme hint, expiry (optional) |
| `kubeconfig` | file content, default context |
| `vpn` | WireGuard config or OpenVPN profile (+ its own credentials) |
| `certificate` | cert chain, private key, passphrase |
| `file` | arbitrary secret file (e.g. `.env`, service account JSON) |
| `note` | secure text |

Features:
- **SSH keys:** generate (ed25519 default, RSA/ECDSA available), import from file or
  `~/.ssh` (OpenSSH, PEM, PuTTY `.ppk`), export public key, copy to server (`ssh-copy-id`
  equivalent).
- **Logins:** password generator, TOTP code generation, copy-to-clipboard with automatic
  clear after N seconds.
- **Import** from KeePass/Bitwarden/1Password exports (later).
- **Encrypted backup/export** of the whole vault or one scope.

## Secret references

Workspace files point to items, never contain them:

```
vault://global/my-ed25519            # whole item
vault://acme/db-admin#password       # one field
```

`opsdeck vault check` / workspace validation reports dangling references.

## Use vs. reveal

The single most important rule, designed now for the AI phase later:

- **use** — the daemon uses the secret on the caller's behalf (signs an SSH challenge,
  sends a password to the server, brings up a VPN). The caller never sees the value.
- **reveal** — the value is returned (show in UI, copy to clipboard, export).

Each item has access settings:

| setting | default |
|---|---|
| `reveal` allowed for | user (UI/CLI, with re-auth optional) |
| `use` allowed for | user, CLI, agent |
| `confirm_on_use` | off (on recommended for prod keys) |
| agent may ever `reveal` | **never** (not configurable for agents) |

Every `use` and `reveal` writes an audit entry: when, which item, which caller
(UI / CLI / MCP-agent), for which workspace/resource, and why.

## Built-in SSH agent (per workspace)

The daemon exposes an `ssh-agent` socket **per workspace**, offering only the keys that
workspace references. Benefits:

- plain `ssh`, `git`, `ansible`, `scp` from any terminal work with vault keys
  (`export SSH_AUTH_SOCK=…` or `opsdeck shell acme`);
- private keys never touch the disk;
- no "Too many authentication failures" from offering 15 keys to every server;
- a key from project A is never offered to a server of project B;
- optional confirm-on-use prompt per key.

## Lock / unlock

- Unlock with master password (later: OS keychain, FIDO2/YubiKey, biometrics via OS).
- Auto-lock after idle timeout and on system sleep/screen lock (configurable).
- On lock: keys are wiped from memory, SSH agents refuse to sign, but already-open
  sessions stay up (configurable: "lock kills sessions").
