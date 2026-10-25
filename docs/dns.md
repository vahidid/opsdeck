# Domains and DNS

opsdeck manages the DNS zones of a workspace's domains at **Cloudflare** and
**ArvanCloud**: the account is connected once on this computer (a *connector*, like the
GitHub / GitLab ones), a workspace lists the zones that belong to it as `Domain`
resources, and their records are read and changed on the **Domains** page or by the
agent's `dns` tool. Every change is recorded in the activity journal.

## Connectors: connect the account once

**Connectors → DNS & CDN** holds the DNS / CDN accounts of this computer. One way in: an
API token created on the provider's page (*Create a token* opens it):

| Provider | Token | API |
|---|---|---|
| Cloudflare | an API token with *Zone → Zone: Read* and *Zone → DNS: Edit* for the zones to manage (the *Edit zone DNS* template); checked with `/user/tokens/verify` and a zone listing | `https://api.cloudflare.com/client/v4`, `Authorization: Bearer` |
| ArvanCloud | an API key (a machine user) with access to the CDN product (domains and DNS records); checked by listing the domains | `https://napi.arvancloud.ir/cdn/4.0`, `Authorization: Apikey` |

The token is stored in the vault (global scope, `vault://global/dns-<connector>`, usable by
the agent, never shown to it) and opsdeck shows what it saw: the account (Cloudflare) and
the number of zones. *Test* checks it again. Under *Advanced*, an **API URL** replaces the
provider's (a mirror or a proxy in front of the API) and a **proxy** (`socks5://` or
`http://`) routes the calls when the provider is not reachable directly from this
computer; without one, the usual `HTTPS_PROXY` settings apply. Redirects are never
followed, so the token never goes to another server.

Reconnecting replaces the token; disconnecting deletes it. The connector id is the
provider's name (`cloudflare`, `arvancloud`, then `cloudflare-2`…).

## Domains of a workspace

**Domains → Add a domain** picks a zone from a connected account (already added ones are
marked) and creates the resource; or add it under Resources. It holds nothing but the
zone and, optionally, the connector:

```yaml
- kind: Domain
  name: shop                    # resource name (lowercase, digits, dashes)
  environment: prod
  spec:
    zone: example.com
    connection: cloudflare      # optional: by default the connected account that has the zone
    notes: |                    # optional, for the agent: what the records belong to
      www and api point at the prod load balancer; mail is at Google Workspace.
```

Without `connection`, opsdeck looks for the zone in every connected account, so workspace
files stay shareable: a colleague with their own Cloudflare login manages the same zone.
A domain in a **protected** environment asks the record's full name to be typed before a
deletion, and the agent's changes to it are conservative (see below).

## The Domains page

The page shows the zone as the provider sees it — status (`active`, `pending`…), plan,
the provider's **name servers** (with a copy button) and a warning when the registrar
still points elsewhere, a link to the provider's dashboard — and the zone's **records**:
type, name (relative to the zone, `@` for the zone itself), content, TTL, whether the
record is **proxied** through the provider's CDN (Cloudflare *proxied*, ArvanCloud
*cloud*), and Cloudflare's comment. Filter by text and type.

*Add record* and a click on a row open the editor: type, name, content (with a hint per
type), TTL (the values ArvanCloud accepts, or *auto* for Cloudflare), priority for MX and
SRV, the CDN toggle for A/AAAA/CNAME, a comment at Cloudflare. Deleting asks first; on a
protected environment the record's full name must be typed. Records the provider manages
itself (ArvanCloud's protected records) are shown with a lock and cannot be changed.

Records come in one shape whatever the provider:

| Type | Content | Notes |
|---|---|---|
| A, AAAA | an address — ArvanCloud: several, comma-separated, balanced over them | Cloudflare takes one address per record |
| CNAME, NS, PTR, ANAME | a host name | |
| MX | the mail server's host name | `priority` separately |
| TXT | the text | |
| SRV | `weight port target` | `priority` separately; the name is like `_sip._tcp` |
| CAA | `flags tag "value"`, e.g. `0 issue "letsencrypt.org"` | |

Other types a zone may hold are listed with the provider's raw value and can be deleted,
not edited.

## The agent's `dns` tool

The tool is offered when the workspace has a `Domain`. It uses the connector's token
(the agent never sees it) and is classified like every other tool:

| Action | | Risk |
|---|---|---|
| `zones` | the workspace's domains with status, name servers and notes | read |
| `records` | a zone's records, filtered by `type` and `name` | read |
| `set` | create a record, or change the one named by `id` or by `type`+`name` (content, ttl, priority, proxied, comment; values left out are kept) | change |
| `delete` | by `id`, or by `type`+`name` when that names exactly one record | destructive |

`set` and `delete` look the record up first, so the approval card shows the zone-file-like
line before and after (`- www.example.com 300 A 203.0.113.10` / `+ … 203.0.113.11`), and
a `set` that would change nothing is refused. Ambiguous names (several records of one type)
ask for the id. The system prompt tells the agent to read the zone first, change one
record at a time, never ask the user for DNS credentials or reach the provider's API with
`http_request`, and to mention that resolvers keep old answers for up to the TTL. Results
come with a records table in the chat.

Permission rules match the action and the record (`update A www.example.com`,
`delete MX example.com`, `create TXT _dmarc.example.com`), so a workspace can for example
allow TXT changes on a staging zone without asking.

## API

See [api.md](api.md#dns-connectors-and-domains): `/api/dns/providers`, `/api/dns/connections`
(list, connect, test, disconnect, zones) and `/api/workspaces/{ws}/domains` (the zones,
records: list, create, update, delete).

## Code

- `internal/dnsx` — the provider-neutral model (`Zone`, `Record`, `RecordInput`), the
  `Provider` interface and its Cloudflare and ArvanCloud implementations (`cloudflare.go`,
  `arvancloud.go`), name and value helpers (`Relative`, `FQDN`, `SRV`, `CAA`, `Format`,
  `Validate`), and the `Manager` that resolves a `Domain` resource to its connector and zone
  (zone ids are cached).
- `internal/server/dns.go` — the connector and domain routes; `config.DNSConnection` holds
  a connector in `config.yaml`.
- `internal/agent/dnstool.go` — the `dns` tool.
- `internal/testutil/dnsserver` — fakes of both APIs for tests: `internal/dnsx/dnsx_test.go`
  (names, values, each provider's bodies), `internal/server/dns_test.go` (connect, zones,
  domains, records, audit, disconnect), `internal/agent/dnstool_test.go` (every action, risks,
  previews, no token in outputs).
