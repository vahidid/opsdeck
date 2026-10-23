# Security policy

opsdeck holds SSH keys, VPN configurations and other credentials, and runs commands on
your servers, so security reports are taken seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Report it privately through
[GitHub's private vulnerability reporting](https://github.com/vahidid/opsdeck/security/advisories/new)
with the affected version, what an attacker can do, and the steps to reproduce it.

Once a fix is released, the advisory is published with credit to you, unless you prefer
otherwise.

## Supported versions

Fixes go into the latest release; opsdeck updates itself from inside the app.

## Scope

Examples of what counts:

- reading vault contents without the master password, or a weakness in the vault's encryption
- secrets reaching the model, the transcript or the audit journal unredacted
- the agent running a change without the approval its mode and rules require
- another website or local user reaching the local API (it listens on 127.0.0.1 only)
- a workspace folder from someone else running commands when it is opened

How the vault and the agent's safeguards are designed: [docs/vault.md](docs/vault.md),
[docs/harness.md](docs/harness.md).
