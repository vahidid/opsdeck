# VPN & networks

A workspace reaches private infrastructure through **networks**: VPN profiles and SOCKS
proxies. They are ordinary workspace resources (`resources/networks.yaml`, no secrets,
`vault://` references only), so a resource reached through one says `via: network/<name>`
and the connection is brought up automatically whenever the UI, the terminal or the
agent needs it.

## Supported VPNs

| Profile kind | Covers | How | Userspace | System (TUN) |
|---|---|---|---|---|
| `WireGuard` | WireGuard | in-process (wireguard-go + gVisor netstack); system via `wg-quick` | ✓ | ✓ (`wg-quick`, `wg`) |
| `Tailscale` | Tailscale, **Headscale** (`controlUrl`) | in-process [`tsnet`](https://pkg.go.dev/tailscale.com/tsnet); auth key or login link | ✓ | — |
| `NetBird` | NetBird cloud or self-hosted | in-process [`client/embed`](https://pkg.go.dev/github.com/netbirdio/netbird/client/embed) | ✓ | ✓ (opsdeck needs root/CAP_NET_ADMIN) |
| `OpenVPN` | OpenVPN | `openvpn` binary + management interface | — | ✓ |
| `OpenConnect` | **Cisco AnyConnect**, **ocserv**, **FortiGate**, Palo Alto GlobalProtect, Pulse/Ivanti, Juniper, F5, Array (`protocol`) | `openconnect` binary; userspace through `ocproxy` | ✓ (`ocproxy`) | ✓ (`vpnc-script`) |
| `OpenFortiVPN` | FortiGate | `openfortivpn` binary (pppd) | — | ✓ |
| `SocksProxy` | any SOCKS5 proxy (e.g. `ssh -D`) | Go SOCKS client | ✓ | — |

The **VPN** page lists what can run on this machine (missing binaries, privileges) and
offers vendor presets: "Cisco AnyConnect", "ocserv", "FortiGate" … map to the right kind
and protocol.

## Modes

**userspace** (default where available) — the TCP/IP stack runs inside opsdeck (gVisor
netstack) or in a helper that exposes a local SOCKS proxy (`ocproxy`). No root, nothing
changes on the machine, many VPNs can be up side by side even with overlapping
subnets. opsdeck's own connections (SSH, panels, Kubernetes, Docker, the agent's tools) go
through the tunnel. Set `proxyPort` to also serve the VPN to other apps as
`socks5h://127.0.0.1:<port>` (browser, `kubectl`, `curl --socks5-hostname`).

**system** — a TUN interface with system routes (and DNS, where the client sets it):
every app on the machine uses the VPN, like the vendor client. It needs root:

- subprocess clients (`openvpn`, `openconnect`, `openfortivpn`, `wg-quick`) run through
  the elevation set in App settings → *Run system-mode VPNs with*: `auto` (`sudo -n` when
  opsdeck is not root), `sudo`, `pkexec` (password dialog on desktops) or `none`. With
  `sudo -n`, allow these binaries in sudoers, e.g.
  `you ALL=(root) NOPASSWD: /usr/sbin/openvpn, /usr/sbin/openconnect, /usr/bin/wg-quick, /usr/bin/wg`.
- in-process system mode (NetBird) needs opsdeck itself to have `CAP_NET_ADMIN`
  (`sudo setcap cap_net_admin,cap_net_raw+ep $(which opsdeck)`).
- in Docker, give the container `/dev/net/tun` and `NET_ADMIN`.

Several system VPNs can conflict on routes and DNS; userspace VPNs cannot.

## Credentials

| Kind | Field | Vault item |
|---|---|---|
| WireGuard | `config` | VPN config (`.conf`) |
| OpenVPN | `config`, `auth` | VPN config or file (`.ovpn` with inline certs); login |
| OpenConnect / OpenFortiVPN | `auth`, `cookie` | login (username, password, optional TOTP secret); token |
| Tailscale | `authKey` | token (pre-auth key); empty = log in through a link |
| NetBird | `setupKey` | token |

Secrets are passed on stdin, through the management socket or in 0600 files inside the
workspace's `.opsdeck/vpn/<name>/run/` (removed on disconnect), never on the command line.
A TOTP secret on the login item answers OTP prompts (`openconnect --token-mode=totp`,
OpenVPN static challenge, `openfortivpn --otp`). Self-signed
gateways are pinned (`serverCert`); the exact pin is shown in the log after the first
attempt. Tailscale and NetBird keep their node keys in `.opsdeck/vpn/<name>/` so the
device keeps its identity. OpenVPN config scripts (`up`/`down`) are disabled unless the
profile sets `allowScripts` (they run as root). Tailscale's log upload is turned off.

## Single sign-on (SAML)

Turn on **Single sign-on** on an OpenConnect or OpenFortiVPN profile (or pick an "· SSO"
preset). On connect opsdeck shows the identity provider's login (Entra ID/Azure AD, Okta,
Google, ADFS, …) and takes the VPN session from it itself; there is nothing to copy:

| Gateway | How opsdeck gets the session |
|---|---|
| Cisco AnyConnect / ASA / FTD, **external-browser** SSO | opsdeck speaks the aggregate-auth XML, sends STRAP keys; the gateway redirects the browser to `http://localhost:29786/api/sso/…` with the token HPKE-encrypted to opsdeck's key (ECDH P-256 + HKDF "AC_ECIES" + AES-GCM); opsdeck trades it for the session cookie |
| Cisco AnyConnect, **embedded-browser** SSO | same, but the token is the `sso-v2-token-cookie-name` cookie read from opsdeck's login window at the final URL |
| Palo Alto GlobalProtect | `prelogin.esp` (portal, then gateway) → SAML REDIRECT/POST → `saml-username` + `prelogin-cookie`/`portal-userauthcookie` from the final response headers, HTML comments or the `globalprotectcallback:` URL |
| FortiGate (openconnect or openfortivpn) | `/remote/saml/start?redirect=1` → callback on `127.0.0.1:8020` → `/remote/saml/auth_id` → `SVPNCOOKIE` (FortiOS 7+), or the `SVPNCOOKIE` set in the login window |

Then the client starts with the cookie on stdin (`--cookie-on-stdin`; GlobalProtect:
`--user --usergroup=portal:… --passwd-on-stdin`). AnyConnect cookies carry the STRAP key in
`openconnect --authenticate`'s format, so openconnect signs the tunnel request with it.

Where the login is shown (profile field *SSO login in*):

- **window** — a Chrome/Edge/Chromium/Brave window opsdeck drives over the DevTools
  protocol (a pipe, not a network port, on Linux/macOS). Every profile has its own browser
  profile in `.opsdeck/vpn/<name>/browser`, so "stay signed in" at the IdP makes later
  logins automatic: the window opens and closes by itself. Required for embedded-browser
  AnyConnect and GlobalProtect (the cookie only exists in the browser that signed in).
- **system** — your default browser, with the link also on the VPN page. Works where the
  gateway calls back to localhost: AnyConnect external-browser SSO and FortiGate.
- **auto** (default) — window when such a browser is installed, otherwise system.

The browser is detected (or set in App settings → *Browser for VPN single sign-on*). While
signing in the profile shows *signing in…*; the agent tells the user to finish the login.
Only one AnyConnect and one FortiGate login can run at a time (fixed callback ports).

## Lifecycle

- *Connect* returns within ~20 s; slower connects continue in the background and show as
  `connecting`, or `waiting for login` with the login link (Tailscale without an auth key).
- A tunnel that drops is shown as `error: connection lost …`; connections through it are
  closed. Each profile keeps a log (last 1000 lines) on the VPN page.
- Locking the vault, deleting or editing a profile, and quitting opsdeck disconnect it.
  Subprocess clients also die with opsdeck (Linux parent-death signal; sudo relays it).

## The agent

The `vpn` tool (`status`, `connect`, `disconnect`, `log`) and the inventory show every
network with its mode and state. The agent is told to connect a VPN when documentation
says something is only reachable over it, when a connection times out and a VPN could
route there, and to hand the user the login link when one is needed.

- userspace *connect* is a read (it only affects opsdeck): no approval needed;
- system *connect* and any *disconnect* are changes; system mode is a protected
  environment, so it always asks — also in Auto mode;
- credentials used by a profile follow the vault policy (agent use, confirm on use).

## Testing

`internal/vpn/sso_test.go` emulates the gateways' SSO (an ASA in external-browser mode with
real STRAP/HPKE, and in embedded mode; GlobalProtect with headers and the callback URL;
FortiGate with the 8020 redirect and the cookie) with an identity provider that signs in
by itself, in a headless Chromium; `TestSSODriver` checks that openconnect presents the
session cookie on its tunnel request. A lab case connects ocserv with a cookie carrying an
opsdeck-generated STRAP key.
`internal/vpn/lab_test.go`, `lab_tailscale_test.go` and `lab_netbird_test.go` run the
drivers against real servers (skipped unless `OPSDECK_VPN_LAB`, `OPSDECK_HEADSCALE` or
`OPSDECK_NETBIRD` is set): ocserv, an OpenVPN server and a WireGuard server in a network
namespace with a private subnet only reachable through a VPN, Headscale and a
self-hosted NetBird server. `internal/server/vpn_test.go` covers the agent flow with an
in-process WireGuard peer and runs everywhere.

Why not other libraries: there is no trustworthy pure-Go OpenVPN client (`ooni/minivpn`
is research-only by its own README; `go-openvpn` needs cgo/openvpn3). `tlslink/sslcon`
(pure-Go OpenConnect) keeps one global connection per process, needs a TUN device and its
module path is not importable, so ocserv goes through `openconnect`, which also gives it a
no-root userspace mode. `openfortivpn-go` is 0.0.x and still shells out to pppd.

Licenses: `tsnet` and the NetBird client are BSD-3; `openvpn`, `openconnect`, `ocproxy`
and `openfortivpn` are separate GPL/LGPL programs that opsdeck runs, not links.
