# Compatibility and versions

opsdeck follows [semantic versioning](https://semver.org). This page says what the
version number promises: which files and interfaces are stable, what a release may change,
and how something is retired.

## What is stable

These are the contracts. From 1.0 on, a MINOR or PATCH release never breaks them; only a
new MAJOR version may, and then with a migration or an upgrade guide.

| Contract | What is covered |
|---|---|
| **Workspace folder** | `workspace.yaml`, `resources/*.yaml`, `topology.yaml`, `OPSDECK.md`, `skills/`, `scripts/`, `agents/`, `commands/`, `docs/` — their keys and meaning ([schema.md](schema.md), [workspace.md](workspace.md)) |
| **Vault** | the vault database and its encryption ([vault.md](vault.md)), the `vault://<scope>/<item>[#field]` references |
| **`opsdeck run`** | its flags, the `json` and `stream-json` outputs (fields are only added), and its exit codes |
| **MCP server** (`opsdeck mcp`, `/mcp/ext`) | tool names and their input fields; the approval behaviour of each [agent access key](agents.md) mode |
| **Skill pack** | the skill names in `plugins/opsdeck` and their install paths |
| **Install and update files** | the release file names in [releasing.md](releasing.md), `SHA256SUMS.txt` and its signature |

Not covered — they may change in any release:

- the local HTTP API under `/api` (it serves the UI; [api.md](api.md) describes it for
  contributors);
- the web UI and the desktop app's windows;
- the agent's prompts, built-in skills and tool descriptions, and the text of tool
  results (they are written for models, not parsers);
- what opsdeck keeps in `.opsdeck/` (session transcripts, caches) and in its data folder,
  other than the vault.

## Versions of the files

**Workspace folder.** `workspace.yaml` starts with `apiVersion: opsdeck/v<N>`; the number
covers every file of the folder.

- A new **optional** key or a new resource kind is not a format change: older opsdeck
  versions ignore what they don't know.
- Renaming, removing or changing the meaning of a key is a format change. It raises the
  number and ships with a migration in `internal/workspace/version.go`. opsdeck reads the
  older format, upgrades it in memory and writes the new format (the whole folder at once)
  the next time it saves the workspace. Commit that change like any other.
- opsdeck refuses to open a folder with a **newer** number than it knows, rather than
  guess and lose data: *written by a newer version of opsdeck — update opsdeck to open it*.
- A `workspace.yaml` without `apiVersion` (written by hand) is format 1.

When a team shares a workspace in git, keep everyone on the same MINOR version: a setting
added in a newer MINOR release is ignored by an older opsdeck, and it is dropped if that
older opsdeck saves `workspace.yaml`. Keys inside a resource's `spec` are always kept.

**Vault.** The vault stores its `format_version`. A newer opsdeck upgrades an older vault
when it opens it; an older opsdeck refuses a newer vault. A vault is never downgraded, so
export a backup before you try a pre-release.

## Deprecation

Something to be retired is first **deprecated**:

1. the release notes list it under *Deprecated*, with what to use instead;
2. it keeps working for at least one MINOR release, and opsdeck reports it (a validation
   warning on the workspace, a warning on stderr for a CLI flag);
3. it is removed only in the next MAJOR release.

## Before 1.0

Versions `0.x` are pre-releases of these contracts: a MINOR release may still change them.
Even so, every change to the workspace or vault format ships with its migration, and the
release notes say what changed under *Changed* or *Removed*.
