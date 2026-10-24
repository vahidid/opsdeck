package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"opsdeck/internal/connect"
	"opsdeck/internal/llm"
	"opsdeck/internal/vpn"
	"opsdeck/internal/workspace"
)

func toolVPN() Tool {
	return Tool{
		Def: llm.Tool{Name: "vpn", Description: "The workspace's VPNs and proxies (WireGuard, Tailscale/Headscale, NetBird, OpenVPN, OpenConnect for Cisco AnyConnect/ocserv/FortiGate/GlobalProtect/Pulse/F5, openfortivpn, SOCKS). action=status lists them with state, mode, address and what is reached through them. action=connect brings one up: resources with via: network/<name> connect it automatically when used, so connect explicitly when the documentation says something is only reachable over a VPN that is not modeled as via, or to check a VPN works. action=disconnect takes one down. action=log shows its recent log, to diagnose a failed connection. Userspace VPNs only affect opsdeck; system-mode VPNs change routes and DNS of the whole computer and always need the user's approval.",
			Schema: obj(map[string]any{
				"action": enum("Action", "status", "connect", "disconnect", "log"),
				"name":   str("Network name (for connect, disconnect, log)"),
				"lines":  num("log: number of lines (default 60)"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action, Name string
				Lines        int
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			if in.Action == "status" {
				return &Call{Risk: RiskRead, Target: "workspace/" + env.WS.Name(), Run: func(ctx context.Context) (string, error) {
					return renderNetworks(env.Hub.Networks(env.WS)), nil
				}}, nil
			}
			r := env.WS.FindRef("network/" + strings.TrimPrefix(in.Name, "network/"))
			if r == nil {
				var names []string
				for _, x := range env.WS.Resources {
					if workspace.IsNetworkKind(x.Kind) {
						names = append(names, x.Name)
					}
				}
				return nil, fmt.Errorf("network %q not found; networks in this workspace: %s", in.Name, strings.Join(names, ", "))
			}
			mode := vpn.ModeOf(r)
			c := &Call{Target: "network/" + r.Name, Match: in.Action + " " + r.Name, Credentials: env.Hub.Credentials(env.WS, r)}
			system := mode == vpn.ModeSystem
			if system {
				c.Environment, c.Protected = "local", true
				c.Reasons = []string{"system mode: changes routes and DNS of this whole computer"}
			}
			switch in.Action {
			case "log":
				c.Risk, c.Credentials = RiskRead, nil
				if in.Lines <= 0 {
					in.Lines = 60
				}
				c.Run = func(ctx context.Context) (string, error) {
					var b strings.Builder
					for _, l := range env.Hub.NetworkLog(env.WS.Name(), r.Name, in.Lines) {
						fmt.Fprintf(&b, "%s %s\n", l.Time.Format("15:04:05"), l.Text)
					}
					if b.Len() == 0 {
						return "(no log yet: the network was not started since opsdeck started)", nil
					}
					return b.String(), nil
				}
			case "connect":
				// A userspace tunnel only affects opsdeck itself (like opening an
				// SSH connection); a system tunnel changes the machine.
				c.Risk, c.Serial = RiskRead, true
				if system {
					c.Risk = RiskChange
				}
				c.Preview = fmt.Sprintf("connect %s (%s, %s mode)", r.Name, r.Kind, mode)
				c.Run = func(ctx context.Context) (string, error) {
					ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
					defer cancel()
					err := env.Hub.NetworkUp(ctx, env.WS, r.Name)
					status := renderNetworks(pick(env.Hub.Networks(env.WS), r.Name))
					if errors.Is(err, connect.ErrConnecting) {
						if strings.Contains(err.Error(), "to choose the") {
							return status, fmt.Errorf("%v. Ask the user to choose it in the prompt opsdeck shows, also on the VPN page (setting it as the profile's group skips this question next time); it connects as soon as they do. Check again with vpn status", err)
						}
						if strings.Contains(err.Error(), "one-time code") {
							return status, fmt.Errorf("%v. Ask the user to enter the code they received (by SMS or email) in the prompt opsdeck shows, also on the VPN page — not in this chat; it connects as soon as they do. Check again with vpn status", err)
						}
						if strings.Contains(err.Error(), "login at") {
							return status, fmt.Errorf("%v. Ask the user to open the login link (also shown on the VPN page); it connects as soon as they log in", err)
						}
						if strings.Contains(err.Error(), "login window") {
							return status, fmt.Errorf("%v. Ask the user to finish the single sign-on in that window; it connects as soon as they are signed in. Check again with vpn status", err)
						}
						return status, fmt.Errorf("%v; check again with vpn status or vpn log", err)
					}
					if err != nil {
						return status, fmt.Errorf("%v (see vpn log %s)", err, r.Name)
					}
					return "connected\n" + status, nil
				}
			case "disconnect":
				c.Risk, c.Credentials = RiskChange, nil
				c.Preview = "disconnect " + r.Name + " (connections through it are closed)"
				c.Run = func(ctx context.Context) (string, error) {
					env.Hub.NetworkDown(env.WS.Name(), r.Name)
					return "disconnected " + r.Name, nil
				}
			default:
				return nil, fmt.Errorf("unknown action %q", in.Action)
			}
			return c, nil
		},
	}
}

func pick(all []connect.NetStatus, name string) []connect.NetStatus {
	for _, n := range all {
		if n.Name == name {
			return []connect.NetStatus{n}
		}
	}
	return nil
}

func renderNetworks(nets []connect.NetStatus) string {
	if len(nets) == 0 {
		return "No VPNs or proxies in this workspace."
	}
	var b strings.Builder
	for _, n := range nets {
		fmt.Fprintf(&b, "- %s (%s", n.Name, n.Kind)
		if n.Mode != "" {
			fmt.Fprintf(&b, ", %s mode", n.Mode)
		}
		fmt.Fprintf(&b, "): %s", n.Status)
		if n.Address != "" {
			fmt.Fprintf(&b, ", address %s", n.Address)
		}
		if n.Interface != "" {
			fmt.Fprintf(&b, ", interface %s", n.Interface)
		}
		if n.Detail != "" {
			fmt.Fprintf(&b, ", %s", n.Detail)
		}
		if n.Proxy != "" {
			fmt.Fprintf(&b, ", SOCKS5 proxy for other apps on %s", n.Proxy)
		}
		if n.Code != nil && len(n.Code.Choices) > 0 {
			fmt.Fprintf(&b, ", waiting for the user to choose the %s (%s)", cmp.Or(n.Code.Label, "choice"), strings.Join(n.Code.Choices, ", "))
		} else if n.Code != nil {
			b.WriteString(", waiting for the user to enter the one-time code the gateway sent")
			if n.Code.Message != "" {
				fmt.Fprintf(&b, " (%s)", n.Code.Message)
			}
		} else if n.AuthURL != "" {
			fmt.Fprintf(&b, ", waiting for login at %s", n.AuthURL)
		} else if n.AuthWindow {
			b.WriteString(", waiting for the user to sign in in the login window")
		}
		if n.Error != "" {
			fmt.Fprintf(&b, ", error: %s", n.Error)
		}
		if n.Missing != "" {
			fmt.Fprintf(&b, ", cannot run here: %s", n.Missing)
		}
		if len(n.UsedBy) > 0 {
			fmt.Fprintf(&b, "; used by %s", strings.Join(n.UsedBy, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}
