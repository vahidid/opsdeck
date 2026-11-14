package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"opsdeck/internal/llm"
)

var hcloudRisk = map[string]string{
	"servers":            RiskRead,
	"server_get":         RiskRead,
	"firewalls":          RiskRead,
	"floating_ips":       RiskRead,
	"reboot":             RiskChange,
	"power_on":           RiskChange,
	"shutdown":           RiskChange,
	"snapshot":           RiskChange,
	"floating_ip_assign": RiskChange,
	"reset":              RiskDestructive,
	"power_off":          RiskDestructive,
}

func hcloudActions() []string {
	return []string{
		"servers", "server_get", "reboot", "reset", "power_on", "power_off",
		"shutdown", "snapshot", "firewalls", "floating_ips", "floating_ip_assign",
	}
}

func toolHCloud() Tool {
	return Tool{
		Def: llm.Tool{Name: "hcloud", Description: `Manage Hetzner Cloud infrastructure directly via the Hetzner Cloud API.
Works even when server SSH is completely unreachable or frozen!
servers: list all cloud servers (ID, name, status, IPv4, datacenter, specs).
server_get: get full details for a server (id or name).
reboot: graceful ACPI reboot of a server.
reset: hard power-cycle reset (essential for frozen/unresponsive servers when SSH is dead).
power_on / power_off / shutdown: control server power state.
snapshot: create an instant backup image of the server before making risky changes.
firewalls: list cloud firewalls and network security rules.
floating_ips: list floating IPs and their assigned servers.
floating_ip_assign: switch a floating IP to another server (failover).
token: Hetzner Cloud API token (or vault:// reference). If omitted, checks vault://global/hcloud-token or Hetzner resources.`,
			Schema: obj(map[string]any{
				"action":      enum("Action to perform", hcloudActions()...),
				"server":      str("Server ID or name (required for server actions)"),
				"description": str("Description/name for snapshot action"),
				"ip":          str("Floating IP ID or address for floating_ip_assign"),
				"token":       str("Optional Hetzner Cloud API token or vault:// reference"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action      string
				Server      string
				Description string
				IP          string
				Token       string
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			in.Action = strings.TrimSpace(in.Action)
			risk, ok := hcloudRisk[in.Action]
			if !ok {
				return nil, fmt.Errorf("unknown hcloud action %q", in.Action)
			}

			// Resolve token
			tok := in.Token
			if tok == "" {
				tok = "vault://global/hcloud-token"
			}
			var resolvedToken string
			if strings.HasPrefix(tok, "vault://") {
				if v, err := env.secretField(tok, "token"); err == nil {
					resolvedToken = v
				}
			} else {
				resolvedToken = tok
			}

			if resolvedToken == "" {
				// Also try finding a resource with provider == Hetzner
				for _, r := range env.WS.Resources {
					if strings.EqualFold(r.Kind, "Panel") && strings.Contains(strings.ToLower(r.Name), "hetzner") {
						if v, err := env.secretField(r.Str("token"), "token"); err == nil {
							resolvedToken = v
							break
						}
					}
				}
			}

			preview := fmt.Sprintf("hcloud %s", in.Action)
			if in.Server != "" {
				preview += " on server " + in.Server
			}
			if in.Action == "reset" {
				preview += "\n⚠️ EMERGENCY HARD RESET: Hardware power-cycle will immediately force restart the server."
			}

			return &Call{
				Risk:        risk,
				Target:      "cloud/hetzner",
				Environment: "cloud",
				Match:       "hcloud " + in.Action,
				Preview:     preview,
				Run: func(ctx context.Context) (string, error) {
					if resolvedToken == "" || strings.HasPrefix(resolvedToken, "vault://") {
						return "", errors.New("no Hetzner Cloud API token found. Provide 'token' or save it in vault://global/hcloud-token")
					}
					client := &hcloudClient{token: resolvedToken}
					return client.run(ctx, in.Action, in.Server, in.Description, in.IP)
				},
			}, nil
		},
	}
}

type hcloudClient struct {
	token string
}

func (c *hcloudClient) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.hetzner.cloud/v1"+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	hc := &http.Client{Timeout: 30 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Hetzner API %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

func (c *hcloudClient) findServerID(ctx context.Context, server string) (string, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", errors.New("server id or name is required")
	}
	// Check if already numeric ID
	isNum := true
	for _, r := range server {
		if r < '0' || r > '9' {
			isNum = false
			break
		}
	}
	if isNum {
		return server, nil
	}
	// Look up by name
	data, err := c.do(ctx, "GET", "/servers?name="+server, nil)
	if err != nil {
		return "", err
	}
	var res struct {
		Servers []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", err
	}
	for _, s := range res.Servers {
		if strings.EqualFold(s.Name, server) {
			return fmt.Sprintf("%d", s.ID), nil
		}
	}
	return "", fmt.Errorf("server %q not found in Hetzner Cloud", server)
}

func (c *hcloudClient) run(ctx context.Context, action, server, desc, ip string) (string, error) {
	switch action {
	case "servers":
		data, err := c.do(ctx, "GET", "/servers", nil)
		if err != nil {
			return "", err
		}
		var res struct {
			Servers []struct {
				ID         int    `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				ServerType struct {
					Name  string `json:"name"`
					Cores int    `json:"cores"`
					Mem   int    `json:"memory"`
					Disk  int    `json:"disk"`
				} `json:"server_type"`
				Datacenter struct {
					Name string `json:"name"`
				} `json:"datacenter"`
				PublicNet struct {
					IPv4 struct {
						IP string `json:"ip"`
					} `json:"ipv4"`
				} `json:"public_net"`
			} `json:"servers"`
		}
		if err := json.Unmarshal(data, &res); err != nil {
			return "", err
		}
		if len(res.Servers) == 0 {
			return "No servers found in this Hetzner Cloud project.", nil
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%-10s %-25s %-10s %-16s %-12s %s\n", "ID", "NAME", "STATUS", "IPV4", "TYPE", "DATACENTER"))
		b.WriteString(strings.Repeat("-", 85) + "\n")
		for _, s := range res.Servers {
			b.WriteString(fmt.Sprintf("%-10d %-25s %-10s %-16s %-12s %s\n",
				s.ID, s.Name, s.Status, s.PublicNet.IPv4.IP, s.ServerType.Name, s.Datacenter.Name))
		}
		return b.String(), nil

	case "server_get":
		sid, err := c.findServerID(ctx, server)
		if err != nil {
			return "", err
		}
		data, err := c.do(ctx, "GET", "/servers/"+sid, nil)
		if err != nil {
			return "", err
		}
		var pretty bytes.Buffer
		json.Indent(&pretty, data, "", "  ")
		return pretty.String(), nil

	case "reboot", "reset", "power_on", "power_off", "shutdown":
		sid, err := c.findServerID(ctx, server)
		if err != nil {
			return "", err
		}
		endpointAction := action
		if action == "power_on" {
			endpointAction = "poweron"
		} else if action == "power_off" {
			endpointAction = "poweroff"
		}
		data, err := c.do(ctx, "POST", fmt.Sprintf("/servers/%s/actions/%s", sid, endpointAction), nil)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Action %s sent successfully to server %s (%s):\n%s", action, server, sid, string(data)), nil

	case "snapshot":
		sid, err := c.findServerID(ctx, server)
		if err != nil {
			return "", err
		}
		if desc == "" {
			desc = fmt.Sprintf("opsdeck-snapshot-%d", time.Now().Unix())
		}
		body := map[string]string{
			"description": desc,
			"type":        "snapshot",
		}
		data, err := c.do(ctx, "POST", fmt.Sprintf("/servers/%s/actions/create_image", sid), body)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Snapshot creation triggered for server %s (%s):\n%s", server, sid, string(data)), nil

	case "firewalls":
		data, err := c.do(ctx, "GET", "/firewalls", nil)
		if err != nil {
			return "", err
		}
		var pretty bytes.Buffer
		json.Indent(&pretty, data, "", "  ")
		return pretty.String(), nil

	case "floating_ips":
		data, err := c.do(ctx, "GET", "/floating_ips", nil)
		if err != nil {
			return "", err
		}
		var pretty bytes.Buffer
		json.Indent(&pretty, data, "", "  ")
		return pretty.String(), nil

	case "floating_ip_assign":
		if ip == "" || server == "" {
			return "", errors.New("both 'ip' and 'server' are required for floating_ip_assign")
		}
		sid, err := c.findServerID(ctx, server)
		if err != nil {
			return "", err
		}
		serverNum := 0
		fmt.Sscanf(sid, "%d", &serverNum)
		data, err := c.do(ctx, "POST", fmt.Sprintf("/floating_ips/%s/actions/assign", ip), map[string]any{
			"server": serverNum,
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Floating IP %s assigned to server %s:\n%s", ip, server, string(data)), nil
	}

	return "", fmt.Errorf("unsupported action %s", action)
}
