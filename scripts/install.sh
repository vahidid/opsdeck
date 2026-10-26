#!/bin/sh
# Installs the latest opsdeck release on Linux or macOS.
#
#   curl -fsSL https://vahidid.github.io/opsdeck/install.sh | sh
#
# Options (after `sh -s --`):
#   --cli            only the CLI (`opsdeck serve`, the UI opens in your browser)
#   --version X.Y.Z  a given release instead of the latest
#   --dir DIR        where the CLI goes (default ~/.local/bin)
#
# The desktop app comes as a .deb or .rpm package on Linux (x86_64) and as an
# app in /Applications on macOS. Every download is checked against the
# release's SHA256SUMS.txt; with minisign installed, so is the signature of
# that file (docs/releasing.md#signatures).
set -eu

REPO=vahidid/opsdeck
# the release key (internal/update/releasekey.go)
RELEASE_KEY=RWRns3uLxhaLFYGyA2gOUMh1YqtebJGtbVhNL1qWwsaoi2g6Mk6g4+XN
mode=desktop
want=latest
bindir="$HOME/.local/bin"

say() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --cli) mode=cli ;;
    --version) [ $# -ge 2 ] || die "--version needs a value"; want=${2#v}; shift ;;
    --dir) [ $# -ge 2 ] || die "--dir needs a value"; bindir=$2; shift ;;
    -h | --help)
      echo "usage: install.sh [--cli] [--version X.Y.Z] [--dir DIR]"
      exit 0
      ;;
    *) die "unknown option $1" ;;
  esac
  shift
done

has curl || die "this script needs curl"
case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported system $(uname -s); on Windows run install.ps1" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m)" ;;
esac

api="https://api.github.com/repos/$REPO/releases/latest"
[ "$want" = latest ] || api="https://api.github.com/repos/$REPO/releases/tags/v$want"
json=$(curl -fsSL -H 'Accept: application/vnd.github+json' "$api") || die "could not read the release from GitHub"
version=$(printf '%s\n' "$json" | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1)
urls=$(printf '%s\n' "$json" | sed -n 's/.*"browser_download_url": *"\([^"]*\)".*/\1/p')
[ -n "$version" ] || die "no release found"
asset() { printf '%s\n' "$urls" | grep -E "$1" | head -n 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

sha256() {
  if has sha256sum; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# fetch URL: downloads into $tmp, checks the checksum, prints the path
fetch() {
  name=${1##*/}
  sums=$(asset '/SHA256SUMS\.txt$')
  [ -n "$sums" ] || die "the release has no SHA256SUMS.txt"
  curl -fsSL -o "$tmp/SHA256SUMS.txt" "$sums" || die "could not download SHA256SUMS.txt"
  if has minisign; then
    sig=$(asset '/SHA256SUMS\.txt\.minisig$')
    [ -n "$sig" ] || die "the release is not signed"
    curl -fsSL -o "$tmp/SHA256SUMS.txt.minisig" "$sig" || die "could not download the signature"
    minisign -Vqm "$tmp/SHA256SUMS.txt" -P "$RELEASE_KEY" || die "the signature of SHA256SUMS.txt does not match the opsdeck release key"
  fi
  curl -fL --progress-bar -o "$tmp/$name" "$1" >&2 || die "could not download $name"
  expected=$(grep " \*\{0,1\}$name\$" "$tmp/SHA256SUMS.txt" | cut -d' ' -f1)
  [ -n "$expected" ] || die "SHA256SUMS.txt has no entry for $name"
  [ "$(sha256 "$tmp/$name")" = "$expected" ] || die "checksum mismatch for $name"
  printf '%s\n' "$tmp/$name"
}

sudo_cmd() {
  if [ "$(id -u)" = 0 ]; then "$@"; elif has sudo; then sudo "$@"; else die "installing needs root (sudo)"; fi
}

install_cli() {
  url=$(asset "/opsdeck-cli-$version-$os-$arch\$")
  [ -n "$url" ] || die "the release has no CLI for $os/$arch"
  say "Downloading the opsdeck $version CLI"
  file=$(fetch "$url")
  mkdir -p "$bindir"
  install -m 755 "$file" "$bindir/opsdeck" 2>/dev/null || sudo_cmd install -m 755 "$file" "$bindir/opsdeck"
  say "Installed $bindir/opsdeck — run: opsdeck"
  case ":$PATH:" in *":$bindir:"*) ;; *) warn "$bindir is not in your PATH" ;; esac
}

install_macos() {
  url=$(asset '-macos-universal\.dmg$')
  [ -n "$url" ] || die "the release has no macOS app"
  say "Downloading opsdeck $version for macOS"
  file=$(fetch "$url")
  dest=/Applications
  [ -w "$dest" ] || dest="$HOME/Applications"
  mkdir -p "$dest" "$tmp/mnt"
  hdiutil attach -nobrowse -readonly -mountpoint "$tmp/mnt" "$file" >/dev/null || die "could not open the disk image"
  rm -rf "$dest/opsdeck.app"
  cp -R "$tmp/mnt/opsdeck.app" "$dest/" || { hdiutil detach "$tmp/mnt" -quiet; die "could not copy the app to $dest"; }
  hdiutil detach "$tmp/mnt" -quiet
  # downloaded by curl, so not quarantined; clear it anyway for older copies
  xattr -dr com.apple.quarantine "$dest/opsdeck.app" 2>/dev/null || true
  say "Installed $dest/opsdeck.app — open it from Launchpad or Spotlight"
}

install_linux() {
  if [ "$arch" != amd64 ]; then
    warn "the desktop app is built for x86_64 only; installing the CLI"
    install_cli
    return
  fi
  if has apt-get || has dpkg; then
    url=$(asset '_amd64\.deb$')
    say "Downloading opsdeck $version (.deb)"
    file=$(fetch "$url")
    chmod 644 "$file" && chmod 755 "$tmp" # apt reads it as _apt
    if has apt-get; then sudo_cmd apt-get install -y "$file"; else sudo_cmd dpkg -i "$file"; fi
  elif has dnf || has zypper || has rpm; then
    url=$(asset '\.x86_64\.rpm$')
    say "Downloading opsdeck $version (.rpm)"
    file=$(fetch "$url")
    if has dnf; then
      sudo_cmd dnf install -y "$file"
    elif has zypper; then
      sudo_cmd zypper --non-interactive install --allow-unsigned-rpm "$file"
    else
      sudo_cmd rpm -U --replacepkgs "$file"
    fi
  else
    warn "no dpkg or rpm here; installing the CLI"
    install_cli
    return
  fi
  say "Installed opsdeck $version — find it in your applications menu, or run: opsdeck-desktop"
}

case "$mode-$os" in
  cli-*) install_cli ;;
  desktop-darwin) install_macos ;;
  desktop-linux) install_linux ;;
esac
