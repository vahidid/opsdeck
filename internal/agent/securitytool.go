package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"opsdeck/internal/llm"
	"opsdeck/internal/sysproc"
)

var secRisk = map[string]string{
	"scan_image":  RiskRead,
	"scan_fs":     RiskRead,
	"scan_config": RiskRead,
	"check_cve":   RiskRead,
}

func secActions() []string {
	return []string{"scan_image", "scan_fs", "scan_config", "check_cve"}
}

func toolSecurityScan() Tool {
	return Tool{
		Def: llm.Tool{Name: "security_scan", Description: `Scan containers, filesystems and configuration files for security vulnerabilities (CVEs) and misconfigurations using Trivy.
scan_image: scan a Docker container image (e.g. nginx:latest, myapp:v1.2) for package vulnerabilities and secrets.
scan_fs: scan a repository or folder on a host/locally for dependency vulnerabilities (in go.sum, package-lock.json, requirements.txt, pom.xml, etc.).
scan_config: scan Dockerfiles or Kubernetes manifests for security misconfigurations (running as root, missing drop capabilities, missing limits).
check_cve: search for a specific CVE ID across a target.
All actions are strictly READ-ONLY. Runs on a workspace host (via SSH) or on this computer if host is omitted.`,
			Schema: obj(map[string]any{
				"action":   enum("Action to perform", secActions()...),
				"target":   str("Target to scan: image name (e.g. redis:7), directory path (e.g. /app or .), or file path (e.g. Dockerfile)"),
				"host":     str("Optional host name to execute scan on; omit to run on this computer"),
				"severity": str("Comma-separated severity levels to report: CRITICAL, HIGH, MEDIUM, LOW (default: CRITICAL,HIGH)"),
				"cve":      str("Optional specific CVE ID to filter for (e.g. CVE-2024-3094)"),
			}, "action", "target")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action   string
				Target   string
				Host     string
				Severity string
				CVE      string
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			in.Action = strings.TrimSpace(in.Action)
			in.Target = strings.TrimSpace(in.Target)
			if in.Action == "" || in.Target == "" {
				return nil, errors.New("action and target are required")
			}
			risk, ok := secRisk[in.Action]
			if !ok {
				return nil, fmt.Errorf("unknown security_scan action %q", in.Action)
			}

			sev := in.Severity
			if sev == "" {
				sev = "CRITICAL,HIGH"
			}

			// Build trivy command
			var subCmd string
			switch in.Action {
			case "scan_image":
				subCmd = fmt.Sprintf("image --severity %s --format table %s", sev, in.Target)
			case "scan_fs":
				subCmd = fmt.Sprintf("fs --severity %s --format table %s", sev, in.Target)
			case "scan_config":
				subCmd = fmt.Sprintf("config --severity %s --format table %s", sev, in.Target)
			case "check_cve":
				subCmd = fmt.Sprintf("image --format table %s", in.Target)
			}

			// Command tries direct 'trivy' CLI first; if not present, runs via container if docker is available
			cmdStr := fmt.Sprintf("if command -v trivy >/dev/null 2>&1; then trivy %s; else docker run --rm -v /var/run/docker.sock:/var/run/docker.sock aquasec/trivy:latest %s; fi", subCmd, subCmd)

			preview := fmt.Sprintf("security_scan %s on %s (severity: %s)", in.Action, in.Target, sev)
			if in.Host != "" {
				preview += "\non host: " + in.Host
			} else {
				preview += "\non this computer"
			}

			return &Call{
				Risk:        risk,
				Target:      "security/" + in.Target,
				Environment: "security",
				Match:       "security_scan " + in.Action + " " + in.Target,
				Preview:     preview,
				Run: func(ctx context.Context) (string, error) {
					timeout := 10 * time.Minute
					ctx, cancel := context.WithTimeout(ctx, timeout)
					defer cancel()

					var out string
					if in.Host != "" {
						h, err := env.host(in.Host)
						if err != nil {
							return "", err
						}
						res, err := env.Hub.Exec(ctx, env.WS, h, cmdStr, nil, 2<<20)
						out = formatExec(res, err)
					} else {
						cmd := sysproc.CommandContext(ctx, "sh", "-c", cmdStr)
						b, err := cmd.CombinedOutput()
						if err != nil && len(b) == 0 {
							return "", fmt.Errorf("scan failed (%v): install trivy CLI or run on a host with Docker", err)
						}
						out = string(b)
					}

					if in.CVE != "" {
						// Filter for specific CVE
						cveLines := []string{"### Scan Results for " + in.CVE + ":"}
						for _, line := range strings.Split(out, "\n") {
							if strings.Contains(strings.ToUpper(line), strings.ToUpper(in.CVE)) {
								cveLines = append(cveLines, line)
							}
						}
						if len(cveLines) == 1 {
							return fmt.Sprintf("✅ **No matches found for %s** in target %s.", in.CVE, in.Target), nil
						}
						return strings.Join(cveLines, "\n"), nil
					}

					return truncate(out, 40000), nil
				},
			}, nil
		},
	}
}
