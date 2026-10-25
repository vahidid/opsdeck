# TLS certificates (ACME)

opsdeck issues and renews TLS certificates from ACME certificate authorities — Let's
Encrypt by default — and keeps them in the vault. Domains are proven with the **dns-01**
challenge through the DNS connectors ([dns.md](dns.md)): a TXT record is created in the
zone, the CA checks it, the record is removed. No web server has to answer a challenge, so
wildcards work and nothing has to be reachable from the internet. The certificate and its
key then go from the vault to the hosts that use them, without the agent ever seeing the
key.

## A certificate is a resource

**Certificates → Add a certificate** (or Resources → *TLS certificate (ACME)*):

```yaml
- kind: Certificate
  name: web
  environment: prod
  spec:
    domains: [example.com, "*.example.com"]   # the first is the common name
    ca: letsencrypt            # letsencrypt | letsencrypt-staging | buypass | buypass-test | custom
    email: ops@example.com     # the ACME account's contact (expiry warnings)
    keyType: ec256             # ec256 | ec384 | rsa2048 | rsa4096
    renewDays: 30              # due for renewal this many days before expiry
    autoRenew: true            # renew by itself while opsdeck runs unlocked
    # directoryUrl: https://ca.example.internal/acme/acme/directory   # ca: custom
    # caCert: |  …             # the custom CA's own TLS certificate, if private
```

Every domain's zone must be reachable through a DNS connector: a `Domain` of the
workspace, or any zone of a connected account (the longest matching zone wins). Nothing
else is needed; the ACME account is created on first use.

| CA | | |
|---|---|---|
| `letsencrypt` | 90-day certificates, wildcards | rate limits per registered domain (50 certificates a week, 5 duplicates) |
| `letsencrypt-staging` | for trying the flow | not trusted by browsers |
| `buypass` | 180-day certificates | no wildcards |
| `buypass-test` | Buypass test environment | not trusted |
| `custom` | any ACME directory (step-ca, an internal CA) | external account binding (ZeroSSL, Google Trust Services) is not supported yet |

## Issuing

*Issue* (or *Renew*) starts a job and shows its log live:

1. the ACME account key of the workspace for that CA (`vault://<ws>/acme-account-<ca>`,
   type *file*; created and registered on first use with the contact email);
2. the zones of every domain are looked up through the connectors before the CA is asked
   for anything;
3. an order; for each name a TXT record `_acme-challenge.<name>` (two for a name and its
   wildcard) through the connector, then a wait until the zone's own name servers answer
   it (up to 4 minutes; when they cannot be queried from this computer, a short pause),
   then the CA validates;
4. a new key (`keyType`), the CSR, the certificate chain; the TXT records are removed,
   also when something failed;
5. the result replaces `vault://<ws>/cert-<name>` (type *certificate*: the chain, the key as
   a secret field, and readable fields — `domains`, `not_before`, `not_after`, `issuer`,
   `serial`, `fingerprint_sha256`, `ca`, `issued_at` — so the page and the agent know its
   state without revealing anything). The item's policy is kept across renewals.

Only one issuance runs per certificate at a time; the job stays for two hours
(`GET /api/workspaces/{ws}/certificates/jobs`). Every issuance is in the activity journal
as `certificate.issue` (started) and `certificate.issued` (finished, by `user`, `system` or
`agent:<session>`).

A certificate is **due** when it is missing, expired, expires within `renewDays`, or no
longer covers the resource's domains (the page and `certificate list` say why). With
`autoRenew`, a due certificate is renewed by itself: opsdeck checks every six hours while it
runs with the vault unlocked. Deploying it to the servers stays a separate, visible step —
ask the agent, or do it on the page.

## Deploying

*Deploy* writes the stored files to a host over SFTP, as the host's SSH user (folders are
created when missing):

| File | Content | Mode |
|---|---|---|
| full chain (default `/etc/ssl/<domain>/fullchain.pem`) | leaf and intermediates — nginx `ssl_certificate`, Caddy, most servers | 0644 |
| private key (default `/etc/ssl/<domain>/privkey.pem`) | the key | 0600 |
| leaf only, intermediates | Apache `SSLCertificateFile` / `SSLCertificateChainFile` | 0644 |
| chain + key | HAProxy's one-file layout | 0600 |

An optional command runs afterwards (presets for nginx, Apache, HAProxy, Caddy, with
`sudo -n`); its output is shown and the whole deployment is audited as
`certificate.deploy`. When the folder belongs to root and the SSH user cannot write there,
prepare it once (`chown`), or let the agent write the files with `sudo` through `exec`.

## The agent's `certificate` tool

Offered when the workspace has a `Certificate`:

| Action | | Risk |
|---|---|---|
| `list` | every certificate: domains, CA, issued or not, expiry, due and why | read |
| `show` | the stored certificate: names, issuer, dates, key type, fingerprint, the public chain | read |
| `issue` | obtain or renew (the whole dns-01 flow; the card names the domains and the CA; the output is the job's log) | change |
| `deploy` | write chain and key to a host (`cert_path`, `key_path`; `leaf_path`, `chain_path`, `bundle_path`), the files listed on the card; reload with `exec` afterwards | change on the host |

The key never enters the model's context: `show` prints the chain only, `deploy` copies the
key from the vault. The system prompt tells the agent to use the tool instead of certbot or
acme.sh with credentials on a host, and to deploy again after a renewal.

## API

See [api.md](api.md#certificates-acme): `/api/acme/cas`, `/api/workspaces/{ws}/certificates`
(status, details with the chain, issue → job, jobs with `since`/`wait`, deploy).

## Code

- `internal/acmex` — `ReadSpec`, the issuance (`Issue`: account, order, dns-01 through
  `dnsx`, CSR, chain, vault item), `Status`/`Inspect`, jobs with live logs, `Deploy`,
  the renewal loop (`Renewals`, `RenewDue`). Built on `golang.org/x/crypto/acme`.
- `internal/server/acme.go` — the routes; `internal/agent/certtool.go` — the tool.
- `internal/testutil/acmeserver` — a fake ACME CA (RFC 8555 flow, dns-01 checked against
  the `dnsserver` fake, certificates signed by an ephemeral root) for
  `internal/acmex/acmex_test.go` (issue, renew in place, due logic, failures clean up),
  `internal/server/acme_test.go` (API, jobs, deploy to an SSH host with modes, audit) and
  `internal/agent/certtool_test.go`.
