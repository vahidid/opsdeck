# Releases and updates

## Versions

opsdeck uses [semantic versioning](https://semver.org): `MAJOR.MINOR.PATCH`, with
pre-releases such as `0.2.0-rc.1`. The version comes from the git tag. CI builds it into
the CLI and the desktop app (`-X main.version=…`) and into the installers' file info.
Builds that aren't from a tag are versioned `<wails.json version>-dev.<commit>`.

## Making a release

1. Add a section for the version at the top of [CHANGELOG.md](../CHANGELOG.md):
   `## [0.2.0] — 2026-11-02`. It becomes the release notes on GitHub, and opsdeck shows
   it in its update dialog.
2. Optionally bump `info.productVersion` in `cmd/opsdeck-desktop/wails.json`. It only
   names the `-dev` builds between releases.
3. Commit and push, then either push a tag:

   ```bash
   git tag v0.2.0
   git push origin main v0.2.0
   ```

   or, on GitHub, open *Actions → Build & release → Run workflow*, pick `main` and
   enter the version (`0.2.0`). The release then creates the `v0.2.0` tag on that commit.

The **Build & release** workflow (`.github/workflows/release.yml`) then:

- builds the desktop app on Linux (.deb, .rpm), Windows (NSIS installer and portable
  .exe, cross-compiled, Authenticode-signed when a certificate is set) and macOS
  (universal .dmg, signed and notarized when a Developer ID is set, else ad-hoc signed);
- builds the CLI for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 and
  windows/amd64;
- writes `SHA256SUMS.txt` over all of them and signs it with the release key
  (`SHA256SUMS.txt.minisig`, see [Signatures](#signatures));
- publishes the GitHub release with the changelog section and an install table.

A tag with a `-` (`v0.2.0-rc.1`) is published as a pre-release. The updater only
looks at the latest full release.

### Release files

| File | For |
|---|---|
| `opsdeck-<v>-windows-x64-setup.exe` | Windows installer: Program Files, Start menu, uninstaller, WebView2 if missing |
| `opsdeck-<v>-windows-x64-portable.exe` | Windows, no install |
| `opsdeck-<v>-macos-universal.dmg` | macOS 12+, Apple silicon and Intel |
| `opsdeck_<v>_amd64.deb` | Debian, Ubuntu (installs `/usr/bin/opsdeck-desktop`) |
| `opsdeck-<v>-1.x86_64.rpm` | Fedora, RHEL, openSUSE |
| `opsdeck-cli-<v>-<os>-<arch>[.exe]` | the CLI (`opsdeck serve`) |
| `SHA256SUMS.txt` | checksums of all of the above |
| `SHA256SUMS.txt.minisig` | the release key's signature of `SHA256SUMS.txt` |

The updater and the install scripts find files by these names, so keep them when you
change the workflow.

## How updates install

`internal/update` asks the GitHub API for the latest release
(`/repos/vahidid/opsdeck/releases/latest`), at most once an hour, and the UI asks the
server every few hours. *App settings → Updates* turns the automatic check off (`updates:
noCheck: true` in `config.yaml`) and has *Check now*. When a newer version is out, the
sidebar shows *Update available*. The update dialog shows the release notes and installs
the update.

Every download is checked against `SHA256SUMS.txt` from the same release, and that file
against its signature: the updater installs nothing from a release that is not signed by
a release key built into it, whose signature names another version, or whose file has no
checksum or a wrong one. Installation depends on how opsdeck was installed:

| Installed as | Update |
|---|---|
| Windows installer (an `uninstall.exe` next to the app) | After opsdeck quits, the new installer runs silently (`/S`) into the same folder; Windows asks for permission. Then opsdeck starts again. |
| Windows portable .exe | The .exe is replaced (the old one is renamed and removed at the next start), and opsdeck restarts. |
| macOS app (`…/opsdeck.app`) | After opsdeck quits, the new app is copied from the disk image next to the old one, swapped in, and started. If anything fails, the old app stays. |
| Linux .deb or .rpm (the file belongs to a package) | `pkexec` installs the package with `apt-get`, `dnf`, `zypper` or `rpm`; the desktop shows a password dialog. Then opsdeck restarts. |
| CLI binary | The binary is replaced in place. Restart `opsdeck serve` to use it. |

Anything else is shown as a manual update with the reason, for example: no permission to
replace the app, a Linux desktop binary that is not from a package, or a development
build. The dialog then links to the release page.

To try the updater without publishing, point it at a stand-in for the GitHub API with
`OPSDECK_UPDATE_API=http://127.0.0.1:…`. It must serve `/repos/vahidid/opsdeck/releases/latest`
and the asset URLs it lists. The tests in `internal/update` do the same.

## Signatures

Each release's `SHA256SUMS.txt` is signed in the [minisign](https://jedisct1.github.io/minisign/)
format (Ed25519). The updater only installs a release whose checksums carry a valid
signature from one of the keys in [`internal/update/keys.go`](../internal/update/keys.go),
with the release's version in the signed comment. `scripts/install.sh` checks the signature
too when `minisign` is installed. The current release key is

```
RWRns3uLxhaLFYGyA2gOUMh1YqtebJGtbVhNL1qWwsaoi2g6Mk6g4+XN
```

Check a download yourself:

```bash
minisign -Vm SHA256SUMS.txt -P RWRns3uLxhaLFYGyA2gOUMh1YqtebJGtbVhNL1qWwsaoi2g6Mk6g4+XN
sha256sum --check --ignore-missing SHA256SUMS.txt
```

### The signing key

The private key is the GitHub Actions secret **`OPSDECK_SIGNING_KEY`** (*Settings → Secrets
and variables → Actions*) and is kept nowhere else but an offline backup. The release job
refuses to publish without it. `tools/releasekey` makes and uses keys:

```bash
go run ./tools/releasekey gen                     # a new key pair
OPSDECK_SIGNING_KEY=… go run ./tools/releasekey sign SHA256SUMS.txt "opsdeck v0.4.0 SHA256SUMS.txt"
go run ./tools/releasekey verify SHA256SUMS.txt   # against the built-in keys
go run ./tools/releasekey pub                     # the current public key
```

`sign` refuses a key the updater doesn't trust.

**Rotating the key.** Make a new pair and add its public key to `ReleaseKeys` next to the
old one; release that version, still signed with the old key. From the next release on,
sign with the new key (replace the secret, and the key in `scripts/install.sh` and in this
file). Drop the old key one release later. Installs older than the release that added the
new key can't verify the new key's signatures and update by hand once.

**If the key leaks**, remove it from `ReleaseKeys` at once, rotate as above without the
overlap, publish a release explaining it, and treat releases signed after the leak as
untrusted.

### Platform signing

These secrets are optional. Without them the build still works and warns.

| Secret | For |
|---|---|
| `MACOS_CERTIFICATE`, `MACOS_CERTIFICATE_PASSWORD` | a *Developer ID Application* certificate as a base64 `.p12`: the app is signed with the hardened runtime |
| `APPLE_ID`, `APPLE_TEAM_ID`, `APPLE_APP_PASSWORD` | notarization with `notarytool` (an app-specific password); the disk image is stapled |
| `WINDOWS_CERTIFICATE`, `WINDOWS_CERTIFICATE_PASSWORD` | a code-signing certificate as a base64 `.pfx`: the installer and the portable `.exe` are signed with `osslsigncode` and timestamped |

The signature on the Windows installer covers the installer; the `opsdeck.exe` it installs
is the unsigned build. An EV or cloud-HSM certificate that can't be exported as a `.pfx`
needs another signing step.
