package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"opsdeck/internal/acmex"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// toolCertificate issues, renews and deploys the workspace's TLS
// certificates (Certificate resources) through ACME and the DNS connectors.
// The key stays in the vault: deploy copies it to a host without the model
// seeing it.
func toolCertificate() Tool {
	return Tool{
		Def: llm.Tool{Name: "certificate", Description: `TLS certificates of the workspace (Certificate resources: domains, an ACME CA such as Let's Encrypt, key type). opsdeck issues them with the dns-01 challenge through the connected DNS accounts and keeps the certificate and its key in the vault — you never see the key. Actions: list (every certificate: domains, CA, issued or not, expiry, whether it is due for renewal), show (the stored certificate: subject, names, issuer, dates, fingerprint, key type, the public chain), issue (obtain or renew the certificate; takes a few minutes: DNS records, propagation, the CA's checks), deploy (write the chain and the key to a host: cert_path gets the full chain, key_path the key with mode 0600; optional leaf_path, chain_path (intermediates) and bundle_path (chain + key, for HAProxy); afterwards reload the service with exec). issue and deploy wait for the user's approval.`,
			Schema: obj(map[string]any{
				"action":      enum("What to do", "list", "show", "issue", "deploy"),
				"certificate": str("Certificate resource name (default: the workspace's only one)"),
				"host":        str("deploy: the host to write to"),
				"cert_path":   str("deploy: path of the full chain file, e.g. /etc/ssl/example.com/fullchain.pem"),
				"key_path":    str("deploy: path of the private key file, e.g. /etc/ssl/example.com/privkey.pem"),
				"leaf_path":   str("deploy: path for the leaf certificate alone (Apache SSLCertificateFile)"),
				"chain_path":  str("deploy: path for the intermediates alone (Apache SSLCertificateChainFile)"),
				"bundle_path": str("deploy: path for chain and key in one file (HAProxy)"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action, Certificate, Host string
				CertPath                  string `json:"cert_path"`
				KeyPath                   string `json:"key_path"`
				LeafPath                  string `json:"leaf_path"`
				ChainPath                 string `json:"chain_path"`
				BundlePath                string `json:"bundle_path"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			if env.ACME == nil {
				return nil, errors.New("certificates are not available")
			}
			if in.Action == "list" {
				return &Call{Risk: RiskRead, Target: "certificates/" + env.WS.Name(), Match: "list", Run: func(ctx context.Context) (string, error) {
					return certList(env), nil
				}}, nil
			}
			r, err := env.certificate(in.Certificate)
			if err != nil {
				return nil, err
			}
			envName, protected := targetInfo(env.WS, r)
			target := "certificate/" + r.Name
			switch in.Action {
			case "show":
				return &Call{Risk: RiskRead, Target: target, Environment: envName, Protected: protected, Match: "show", Run: func(ctx context.Context) (string, error) {
					st := env.ACME.Status(env.WS, r)
					var b strings.Builder
					b.WriteString(certLine(st) + "\n")
					if !st.Issued {
						b.WriteString("Not issued yet: use issue.\n")
						return b.String(), nil
					}
					i := st.Info
					fmt.Fprintf(&b, "subject: %s\nnames: %s\nissuer: %s\nvalid: %s → %s (%d days left)\nkey: %s\nserial: %s\nsha256: %s\nchain: %d certificates\nvault item: %s (the key is in it; deploy copies it to a host)\n",
						i.Subject, strings.Join(i.Domains, ", "), i.Issuer, i.NotBefore.Format(time.RFC3339), i.NotAfter.Format(time.RFC3339), i.DaysLeft, i.KeyType, i.Serial, i.Fingerprint, i.Chain, st.Item)
					if it, _, err := env.Vault.ResolveFor(env.WS.Name(), st.Item); err == nil {
						b.WriteString("\n" + it.Get("certificate"))
					}
					return b.String(), nil
				}}, nil
			case "issue":
				spec, err := acmex.ReadSpec(r)
				if err != nil {
					return nil, err
				}
				st := env.ACME.Status(env.WS, r)
				verb := "issue"
				if st.Issued {
					verb = "renew"
				}
				preview := fmt.Sprintf("%s the certificate %s for %s from %s (dns-01 through the DNS connectors; the result goes to %s)", verb, r.Name, strings.Join(spec.Domains, ", "), spec.CA.Label, st.Item)
				if st.Issued && !st.Due {
					preview += fmt.Sprintf("\nnote: the current certificate is valid for %d more days", st.Info.DaysLeft)
				}
				return &Call{Risk: RiskChange, Target: target, Environment: envName, Protected: protected, Match: verb + " " + strings.Join(spec.Domains, " "), Preview: preview,
					Credentials: env.dnsCredentialsAll(), Run: func(ctx context.Context) (string, error) {
						j, err := env.ACME.Start(env.WS, r, "agent:"+env.SessionID)
						if err != nil {
							return "", err
						}
						wctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
						defer cancel()
						v := j.Wait(wctx)
						out := strings.Join(v.Lines, "\n")
						if v.State == "running" {
							return out + "\n\nStill running (job " + j.ID + "); check again with list.", nil
						}
						if v.State == "error" {
							return out, errors.New(v.Error)
						}
						return out + "\n\nDeploy it where it is used (deploy), then reload the service.", nil
					}}, nil
			case "deploy":
				h, err := env.host(in.Host)
				if err != nil {
					return nil, err
				}
				o := acmex.DeployOptions{CertPath: in.CertPath, KeyPath: in.KeyPath, LeafPath: in.LeafPath, ChainPath: in.ChainPath, BundlePath: in.BundlePath}
				if len(o.Files()) == 0 {
					spec, err := acmex.ReadSpec(r)
					if err != nil {
						return nil, err
					}
					o = acmex.DefaultPaths(spec.Domains[0])
				}
				if err := o.Validate(); err != nil {
					return nil, err
				}
				st := env.ACME.Status(env.WS, r)
				if !st.Issued {
					return nil, fmt.Errorf("certificate %s is not issued yet: issue it first", r.Name)
				}
				hEnv, hProt := targetInfo(env.WS, h, r)
				var lines []string
				for _, f := range o.Files() {
					kind := "full chain"
					switch f {
					case o.KeyPath:
						kind = "private key (0600)"
					case o.LeafPath:
						kind = "leaf certificate"
					case o.ChainPath:
						kind = "intermediates"
					case o.BundlePath:
						kind = "chain + key (0600)"
					}
					lines = append(lines, f+"  ← "+kind)
				}
				preview := fmt.Sprintf("write the certificate %s (%s, until %s) to %s:\n%s", r.Name, strings.Join(st.Info.Domains, ", "), st.Info.NotAfter.Format("2006-01-02"), h.Name, strings.Join(lines, "\n"))
				return &Call{Risk: RiskChange, Target: "host/" + h.Name, Environment: hEnv, Protected: hProt, Match: "deploy " + r.Name + " " + strings.Join(o.Files(), " "), Preview: preview,
					Credentials: append(env.creds(h), st.Item), Run: func(ctx context.Context) (string, error) {
						written, err := env.ACME.Deploy(ctx, env.Hub, env.WS, r.Name, h, o)
						if err != nil {
							if len(written) > 0 {
								return "written: " + strings.Join(written, ", ") + "\n", err
							}
							return "", err
						}
						return "Written on " + h.Name + ":\n" + strings.Join(written, "\n") + "\n\nReload the service that uses them (exec), then check it serves the new certificate (net_check type=http, or openssl s_client).", nil
					}}, nil
			}
			return nil, fmt.Errorf("unknown action %q (list, show, issue, deploy)", in.Action)
		},
	}
}

// certificate finds a Certificate resource by name (or the only one).
func (e *Env) certificate(name string) (*workspace.Resource, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "certificate/")
	var all []*workspace.Resource
	for _, r := range e.WS.Resources {
		if r.Kind == "Certificate" {
			all = append(all, r)
		}
	}
	if len(all) == 0 {
		return nil, errors.New("this workspace has no certificates: add one (kind Certificate with its domains) with workspace_resource or on the Certificates page")
	}
	if name == "" && len(all) == 1 {
		return all[0], nil
	}
	var names []string
	for _, r := range all {
		if r.Name == name {
			return r, nil
		}
		names = append(names, r.Name)
	}
	if name == "" {
		return nil, fmt.Errorf("name one of the certificates: %s", strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("certificate %q not found; certificates: %s", name, strings.Join(names, ", "))
}

// dnsCredentialsAll lists every DNS connector's token (an issuance may use
// any of them).
func (e *Env) dnsCredentialsAll() []string {
	if e.DNS == nil || e.DNS.Connections == nil {
		return nil
	}
	var refs []string
	for _, c := range e.DNS.Connections() {
		refs = append(refs, c.Credential)
	}
	return refs
}

func certLine(st acmex.Status) string {
	line := fmt.Sprintf("- %s: %s from %s", st.Name, strings.Join(st.Domains, ", "), st.CALabel)
	if st.Environment != "" {
		line += " env=" + st.Environment
		if st.Protected {
			line += " (PROTECTED)"
		}
	}
	switch {
	case st.Error != "":
		line += " — invalid: " + st.Error
	case !st.Issued:
		line += " — not issued yet"
	default:
		line += fmt.Sprintf(" — issued by %s, expires %s (%d days)", st.Info.Issuer, st.Info.NotAfter.Format("2006-01-02"), st.Info.DaysLeft)
		if st.Due {
			line += " — DUE: " + st.Reason
		}
	}
	if st.AutoRenew {
		line += " [auto-renew]"
	}
	if st.Job != "" {
		line += " [issuing now]"
	}
	return line
}

func certList(env *Env) string {
	var b strings.Builder
	n := 0
	for _, r := range env.WS.Resources {
		if r.Kind != "Certificate" {
			continue
		}
		n++
		b.WriteString(certLine(env.ACME.Status(env.WS, r)) + "\n")
	}
	if n == 0 {
		return "No certificates in this workspace (add a Certificate resource with its domains)."
	}
	return b.String()
}
