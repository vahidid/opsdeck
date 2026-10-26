package main

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"opsdeck/internal/testutil/fakellm"
	"opsdeck/internal/testutil/sshd"
)

// The demo (-demo) is the workspace of the README screenshots: an e-commerce
// shop with a bastion, web, API, worker and database hosts (local SSH
// servers, each its own folder), a Compose project on the web hosts (a fake
// docker CLI on their PATH), vault items, an Ansible project and, with
// -postgres, a real database. Its scripted agent investigates a 502.

// fakeDocker answers the docker commands opsdeck runs. A host is told apart
// by its folder ($PWD); the Compose project lives in $SRV.
const fakeDocker = `#!/bin/sh
host=$(basename "$PWD")
echo "$host docker $*" >> "$DEMO_LOG"
ts() { date -u -d "-$1 seconds" +%Y-%m-%dT%H:%M:%S.000000000Z 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ; }
container() { # id name image service state status ports
  printf '{"ID":"%s","Names":"%s","Image":"%s","Command":"\\"/entrypoint.sh\\"","CreatedAt":"2026-10-21 09:12:44 +0000 UTC","RunningFor":"4 days ago","Ports":"%s","State":"%s","Status":"%s","Size":"0B","Labels":"com.docker.compose.project=shop,com.docker.compose.service=%s,com.docker.compose.project.config_files=%s/compose.yaml,com.docker.compose.project.working_dir=%s","Mounts":"","Networks":"shop_default","LocalVolumes":"1"}\n' "$1" "$2" "$3" "$7" "$5" "$6" "$4" "$SRV" "$SRV"
}
containers() {
  container 4f1c2a9b7e10 shop-web-1 ghcr.io/acme/shop-web:2.14.1 web running "Up 4 days" "0.0.0.0:80->8080/tcp"
  container 9a7d3e5f1c22 shop-api-1 ghcr.io/acme/shop-api:2.14.1 api running "Up 2 minutes" "8081/tcp"
  container c3e8b1d4a933 shop-worker-1 ghcr.io/acme/shop-worker:2.14.1 worker running "Up 4 days" ""
  container 7b2f9c6e0d44 shop-cache-1 redis:7.4-alpine cache running "Up 4 days (healthy)" "6379/tcp"
  container e5a1f7b3c855 shop-proxy-1 caddy:2.8 proxy running "Up 4 days" "0.0.0.0:443->443/tcp"
}
case "$*" in
  "version --format"*|version*) echo 27.3.1 ;;
  "compose version"*) echo 2.29.7 ;;
  "info --format"*) echo '{"ServerVersion":"27.3.1","Containers":5,"ContainersRunning":5,"Images":7,"OperatingSystem":"Ubuntu 24.04.1 LTS","NCPU":4,"MemTotal":8327245824,"Swarm":{"LocalNodeState":"inactive"}}' ;;
  "compose ls"*) printf '[{"Name":"shop","Status":"running(5)","ConfigFiles":"%s/compose.yaml"}]\n' "$SRV" ;;
  *"ps "*table*|*" ps"*table*)
    printf 'NAME            IMAGE                              STATUS\n'
    printf 'shop-api-1      ghcr.io/acme/shop-api:2.14.1       Up 2 minutes\n'
    printf 'shop-cache-1    redis:7.4-alpine                   Up 4 days (healthy)\n'
    printf 'shop-proxy-1    caddy:2.8                          Up 4 days\n'
    printf 'shop-web-1      ghcr.io/acme/shop-web:2.14.1       Up 4 days\n'
    printf 'shop-worker-1   ghcr.io/acme/shop-worker:2.14.1    Up 4 days\n' ;;
  "compose"*" ps "*json*)
    printf '{"ID":"4f1c2a9b7e10","Name":"shop-web-1","Service":"web","Image":"ghcr.io/acme/shop-web:2.14.1","State":"running","Status":"Up 4 days"}\n'
    printf '{"ID":"9a7d3e5f1c22","Name":"shop-api-1","Service":"api","Image":"ghcr.io/acme/shop-api:2.14.1","State":"running","Status":"Up 2 minutes"}\n'
    printf '{"ID":"c3e8b1d4a933","Name":"shop-worker-1","Service":"worker","Image":"ghcr.io/acme/shop-worker:2.14.1","State":"running","Status":"Up 4 days"}\n'
    printf '{"ID":"7b2f9c6e0d44","Name":"shop-cache-1","Service":"cache","Image":"redis:7.4-alpine","State":"running","Status":"Up 4 days (healthy)"}\n'
    printf '{"ID":"e5a1f7b3c855","Name":"shop-proxy-1","Service":"proxy","Image":"caddy:2.8","State":"running","Status":"Up 4 days"}\n' ;;
  "ps"*) containers ;;
  "stats"*)
    echo '{"ID":"4f1c2a9b7e10","Name":"shop-web-1","CPUPerc":"3.41%","MemUsage":"212MiB / 7.75GiB","MemPerc":"2.67%","NetIO":"1.92GB / 2.40GB","BlockIO":"41MB / 2MB","PIDs":"24"}'
    echo '{"ID":"9a7d3e5f1c22","Name":"shop-api-1","CPUPerc":"18.72%","MemUsage":"486MiB / 7.75GiB","MemPerc":"6.12%","NetIO":"812MB / 1.1GB","BlockIO":"12MB / 0B","PIDs":"41"}'
    echo '{"ID":"c3e8b1d4a933","Name":"shop-worker-1","CPUPerc":"7.05%","MemUsage":"301MiB / 7.75GiB","MemPerc":"3.79%","NetIO":"95MB / 70MB","BlockIO":"3MB / 0B","PIDs":"12"}'
    echo '{"ID":"7b2f9c6e0d44","Name":"shop-cache-1","CPUPerc":"0.82%","MemUsage":"38MiB / 7.75GiB","MemPerc":"0.48%","NetIO":"402MB / 380MB","BlockIO":"0B / 18MB","PIDs":"6"}'
    echo '{"ID":"e5a1f7b3c855","Name":"shop-proxy-1","CPUPerc":"0.51%","MemUsage":"24MiB / 7.75GiB","MemPerc":"0.30%","NetIO":"2.6GB / 2.7GB","BlockIO":"1MB / 0B","PIDs":"9"}' ;;
  "images"*)
    for i in shop-web shop-api shop-worker; do printf '{"ID":"sha256:%s","Repository":"ghcr.io/acme/%s","Tag":"2.14.1","Size":"184MB","CreatedSince":"5 days ago"}\n' "$(echo $i | md5sum | cut -c1-12)" "$i"; done
    echo '{"ID":"sha256:5f1a2b3c4d5e","Repository":"redis","Tag":"7.4-alpine","Size":"41MB","CreatedSince":"3 weeks ago"}'
    echo '{"ID":"sha256:6a7b8c9d0e1f","Repository":"caddy","Tag":"2.8","Size":"48MB","CreatedSince":"2 months ago"}' ;;
  "volume ls"*) echo '{"Name":"shop_cache","Driver":"local","Mountpoint":"/var/lib/docker/volumes/shop_cache/_data"}'; echo '{"Name":"shop_uploads","Driver":"local","Mountpoint":"/var/lib/docker/volumes/shop_uploads/_data"}' ;;
  "network ls"*) echo '{"ID":"a1b2c3d4e5f6","Name":"shop_default","Driver":"bridge","Scope":"local"}'; echo '{"ID":"b2c3d4e5f6a7","Name":"bridge","Driver":"bridge","Scope":"local"}' ;;
  "system df"*) echo '{"Type":"Images","TotalCount":"7","Active":"5","Size":"689MB","Reclaimable":"96MB (13%)"}'; echo '{"Type":"Containers","TotalCount":"5","Active":"5","Size":"12MB","Reclaimable":"0B (0%)"}'; echo '{"Type":"Local Volumes","TotalCount":"2","Active":"2","Size":"1.2GB","Reclaimable":"0B (0%)"}' ;;
  *" config"*) cat "$SRV/compose.yaml" ;;
  *" up "*|*" up"|*restart*|*recreate*) echo " Container shop-api-1  Recreated"; echo " Container shop-api-1  Started" ;;
  *logs*)
    n=0
    while read -r line; do n=$((n+1)); printf '%s %s\n' "$(ts $((200-n*3)))" "$line"; done <<'EOF'
api  | INFO  starting shop-api 2.14.1 (go1.23.2)
api  | INFO  config: DATABASE_URL=postgres://shop@db-old:5432/shop
api  | INFO  listening on :8081
api  | WARN  db: dial tcp: lookup db-old on 127.0.0.11:53: no such host (retry 1/5)
web  | 10.20.1.5 - - "GET /api/cart HTTP/1.1" 502 157 "-" "Mozilla/5.0"
api  | WARN  db: dial tcp: lookup db-old on 127.0.0.11:53: no such host (retry 2/5)
web  | 10.20.1.5 - - "GET /api/products?page=2 HTTP/1.1" 502 157 "-" "Mozilla/5.0"
api  | WARN  db: dial tcp: lookup db-old on 127.0.0.11:53: no such host (retry 3/5)
worker | INFO  queue: 0 jobs waiting
web  | 10.20.1.7 - - "GET /healthz HTTP/1.1" 200 2 "-" "kube-probe/1.31"
api  | ERROR db: giving up after 5 retries: lookup db-old: no such host
api  | ERROR GET /api/cart: 500 (no database connection) 3ms
web  | 10.20.1.5 - - "GET /api/cart HTTP/1.1" 502 157 "-" "Mozilla/5.0"
api  | ERROR GET /api/checkout: 500 (no database connection) 2ms
web  | 10.20.1.9 - - "POST /api/checkout HTTP/1.1" 502 157 "-" "Mozilla/5.0"
cache | INFO  DB 0: 1824 keys (1203 volatile)
EOF
    ;;
  *) echo "docker $*: not in the demo" >&2; exit 1 ;;
esac
`

// fakeJournal is journalctl with the last minutes of a web host.
const fakeJournal = `#!/bin/sh
echo "$(basename "$PWD") journalctl $*" >> "$DEMO_LOG"
cat <<'EOF'
{"__REALTIME_TIMESTAMP":"1761400000000000","_HOSTNAME":"web-1","SYSLOG_IDENTIFIER":"systemd","PRIORITY":"6","MESSAGE":"Started docker-shop.service - shop compose project."}
{"__REALTIME_TIMESTAMP":"1761400005000000","_HOSTNAME":"web-1","SYSLOG_IDENTIFIER":"dockerd","PRIORITY":"6","MESSAGE":"container shop-api-1 restarted (exit 1)"}
{"__REALTIME_TIMESTAMP":"1761400012000000","_HOSTNAME":"web-1","SYSLOG_IDENTIFIER":"kernel","PRIORITY":"4","MESSAGE":"TCP: request_sock_TCP: Possible SYN flooding on port 443. Sending cookies."}
{"__REALTIME_TIMESTAMP":"1761400020000000","_HOSTNAME":"web-1","SYSLOG_IDENTIFIER":"sshd","PRIORITY":"6","MESSAGE":"Accepted publickey for deploy from 203.0.113.10 port 51522 ssh2: ED25519"}
{"__REALTIME_TIMESTAMP":"1761400031000000","_HOSTNAME":"web-1","SYSLOG_IDENTIFIER":"dockerd","PRIORITY":"3","MESSAGE":"health check for container shop-api-1 failed: dial tcp 172.18.0.3:8081: connect: connection refused"}
EOF
`

const composeYAML = `name: shop
services:
  proxy:
    image: caddy:2.8
    ports: ["443:443"]
    depends_on: [web]
  web:
    image: ghcr.io/acme/shop-web:2.14.1
    ports: ["80:8080"]
    env_file: .env
  api:
    image: ghcr.io/acme/shop-api:2.14.1
    env_file: .env
    restart: unless-stopped
  worker:
    image: ghcr.io/acme/shop-worker:2.14.1
    env_file: .env
  cache:
    image: redis:7.4-alpine
    volumes: [cache:/data]
volumes:
  cache: {}
`

const envFile = `APP_ENV=production
DATABASE_URL=postgres://shop@db-old:5432/shop
REDIS_URL=redis://cache:6379/0
LOG_LEVEL=info
`

// demoAgent is the scripted incident: look, find the cause, fix it (waits for
// approval on prod), check, report.
func demoAgent(srv string) *fakellm.Server {
	fixed := strings.Replace(envFile, "db-old:5432", "db-1:5432", 1)
	return fakellm.New([]fakellm.Turn{
		{Text: "I'll start with the inventory and the shop's containers on web-1.", Tools: []fakellm.ToolCall{
			{Name: "inventory", Args: map[string]any{}},
			{Name: "docker", Args: map[string]any{"action": "compose_ps", "host": "web-1", "project": "shop"}},
		}},
		{Text: "All containers are up, but `shop-api-1` restarted two minutes ago. Its logs:", Tools: []fakellm.ToolCall{
			{Name: "docker", Args: map[string]any{"action": "compose_logs", "host": "web-1", "project": "shop", "service": "api", "tail": 50}},
		}},
		{Text: "The API can't resolve `db-old`: every request that needs the database fails, and the proxy answers 502. The database host in the inventory is `db-1`. Let me check the project's `.env`.", Tools: []fakellm.ToolCall{
			{Name: "docker", Args: map[string]any{"action": "compose_file", "host": "web-1", "project": "shop", "file": srv + "/.env"}},
		}},
		{Text: "`DATABASE_URL` still points at `db-old`, which was retired last week. I'll point it at `db-1` and restart the project. This changes production, so it waits for your approval.", Tools: []fakellm.ToolCall{
			{Name: "docker", Args: map[string]any{"action": "compose_write", "host": "web-1", "project": "shop", "file": srv + "/.env", "content": fixed, "deploy": true}},
		}},
		{Text: "## The shop is back\n\n**Cause.** `DATABASE_URL` in `" + srv + "/.env` on web-1 still pointed at `db-old`, retired last week. The API could not reach its database, so every page that needs it returned 502.\n\n**Fix.** Pointed it at `db-1:5432` and restarted the project (approved by you).\n\n**Check.** `shop-api-1` connects to the database; `/api/cart` answers 200 again.\n\n**Follow-up.** web-2 uses the same `.env`: worth checking before its next deploy."},
	})
}

// random and otpSecret make the demo's credentials, so none are in the code.
func random(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func otpSecret() string {
	b := make([]byte, 10)
	rand.Read(b)
	return base32.StdEncoding.EncodeToString(b)
}

// seedDemo fills the workspace for the screenshots.
func seedDemo(c *client, home, postgres string) *fakellm.Server {
	srv := filepath.Join(home, "srv", "shop")
	if s := os.Getenv("OPSDECK_DEMO_SRV"); s != "" {
		srv = s // e.g. /srv/shop, so the screenshots show a familiar path
	}
	bin := filepath.Join(home, "bin")
	for _, d := range []string{srv, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(srv, "compose.yaml"), []byte(composeYAML), 0o644)
	os.WriteFile(filepath.Join(srv, ".env"), []byte(envFile), 0o644)
	os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeDocker), 0o755)
	os.WriteFile(filepath.Join(bin, "journalctl"), []byte(fakeJournal), 0o755)
	// a login shell shows the host's name in its prompt
	os.WriteFile(filepath.Join(home, ".bash_profile"), []byte(`export PS1='\[\e[1;32m\]deploy@'"$(basename "$PWD")"'\[\e[0m\]:\[\e[1;34m\]~\[\e[0m\]$ '`+"\n"), 0o644)
	os.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	os.Setenv("SRV", srv)
	os.Setenv("DEMO_LOG", filepath.Join(home, "demo.log"))
	// the hosts' login shells read home/.bash_profile; Python packages
	// installed for the user (ansible) stay visible
	if h, err := os.UserHomeDir(); err == nil && os.Getenv("PYTHONUSERBASE") == "" {
		os.Setenv("PYTHONUSERBASE", filepath.Join(h, ".local"))
	}
	os.Setenv("HOME", home)

	c.do("POST", "/api/workspaces", map[string]string{"name": "acme", "title": "ACME Shop", "description": "E-commerce platform: production and staging", "color": "#10b981"}, nil)
	c.do("PUT", "/api/workspaces/acme", map[string]any{"title": "ACME Shop", "description": "E-commerce platform: production and staging", "color": "#10b981",
		"environments": []map[string]any{{"name": "dev"}, {"name": "staging"}, {"name": "prod", "protected": true}}}, nil)
	var key struct{ PublicKey string }
	c.do("POST", "/api/vault/ssh/generate", map[string]any{"scope": "acme", "name": "deploy-key", "algorithm": "ed25519"}, &key)
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.PublicKey))
	if err != nil {
		log.Fatalf("ssh key: %v", err)
	}

	// the hosts behind the bastion get private addresses, which the bastion
	// forwards to their local SSH servers
	hosts := []struct {
		name, env, addr string
		roles           []string
		tags            []string
	}{
		{"web-1", "prod", "10.20.1.11", []string{"web", "docker"}, []string{"shop"}},
		{"web-2", "prod", "10.20.1.12", []string{"web", "docker"}, []string{"shop"}},
		{"api-1", "prod", "10.20.1.21", []string{"app"}, nil},
		{"worker-1", "prod", "10.20.1.31", []string{"worker"}, nil},
		{"db-1", "prod", "10.20.2.5", []string{"db"}, []string{"postgres-16"}},
		{"stg-app-1", "staging", "10.30.1.10", []string{"app", "docker"}, nil},
	}
	local := map[string]string{}
	opts := func(name string) sshd.Options {
		dir := filepath.Join(home, "hosts", name)
		os.MkdirAll(dir, 0o755)
		return sshd.Options{AuthorizedKeys: []ssh.PublicKey{pub}, Dir: dir}
	}
	for _, h := range hosts {
		s, err := sshd.Start("127.0.0.1:0", opts(h.name))
		if err != nil {
			log.Fatal(err)
		}
		local[h.addr+":22"] = s.Addr
	}
	bo := opts("bastion")
	bo.Forward = func(addr string) string {
		if l, ok := local[addr]; ok {
			return l
		}
		return addr
	}
	bastion, err := sshd.Start("127.0.0.1:0", bo)
	if err != nil {
		log.Fatal(err)
	}
	c.do("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "bastion", "environment": "prod", "tags": []string{"edge"},
		"spec": map[string]any{"address": "127.0.0.1", "port": portOf(bastion.Addr), "user": "deploy", "auth": "vault://acme/deploy-key", "roles": []string{"bastion"}}}, nil)
	for _, h := range hosts {
		c.do("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": h.name, "environment": h.env, "tags": h.tags, "via": "host/bastion",
			"spec": map[string]any{"address": h.addr, "user": "deploy", "auth": "vault://acme/deploy-key", "roles": h.roles}}, nil)
	}
	c.do("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "HostGroup", "name": "web", "spec": map[string]any{"hosts": []string{"web-1", "web-2"}}}, nil)
	c.do("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Panel", "name": "grafana", "environment": "prod", "via": "host/bastion", "spec": map[string]any{"url": "http://10.20.1.40:3000"}}, nil)
	c.do("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "GitRemote", "name": "shop", "spec": map[string]any{"url": "https://gitlab.com/acme/shop", "provider": "gitlab"}}, nil)
	c.do("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "vpn", "name": "office-wg", "fields": []map[string]any{{"name": "config", "value": `[Interface]
PrivateKey = ` + random(32) + `
Address = 10.99.0.2/32

[Peer]
PublicKey = ` + random(32) + `
Endpoint = vpn.acme.example:51820
AllowedIPs = 10.20.0.0/16
`}}}, nil)
	c.do("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "WireGuard", "name": "office", "description": "Office network into the prod VPC",
		"spec": map[string]any{"config": "vault://acme/office-wg", "mode": "userspace"}}, nil)

	// vault: logins with a one-time code, an API token
	c.do("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "login", "name": "grafana-admin", "tags": []string{"grafana"}, "fields": []map[string]any{
		{"name": "url", "value": "https://grafana.acme.example"}, {"name": "username", "value": "admin"}, {"name": "password", "value": random(12)},
		{"name": "totp", "value": "otpauth://totp/Grafana:admin?secret=" + otpSecret() + "&issuer=Grafana"}}}, nil)
	c.do("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "login", "name": "gitlab", "fields": []map[string]any{
		{"name": "url", "value": "https://gitlab.com"}, {"name": "username", "value": "ops@acme.example"}, {"name": "password", "value": random(12)},
		{"name": "totp", "value": "otpauth://totp/GitLab:ops?secret=" + otpSecret() + "&issuer=GitLab"}}}, nil)
	c.do("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "token", "name": "cloudflare-dns", "fields": []map[string]any{{"name": "token", "value": random(30)}}}, nil)

	if postgres != "" {
		// user:password@host:port/db
		cred, addr, _ := strings.Cut(postgres, "@")
		user, pw, _ := strings.Cut(cred, ":")
		hostport, db, _ := strings.Cut(addr, "/")
		c.do("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "login", "name": "shop-db", "fields": []map[string]any{
			{"name": "username", "value": user}, {"name": "password", "value": pw}}}, nil)
		c.do("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Database", "name": "shop-db", "environment": "prod",
			"spec": map[string]any{"engine": "postgres", "address": hostport, "database": db, "login": "vault://acme/shop-db"}}, nil)
	}

	// an Ansible project with a playbook
	c.do("POST", "/api/workspaces/acme/ansible/projects", map[string]any{"name": "infra", "source": "folder", "scaffold": true, "inventory": "workspace"}, nil)
	c.do("PUT", "/api/workspaces/acme/ansible/p/infra/file", map[string]any{"path": "site.yml", "content": `- name: Web servers
  hosts: web
  become: true
  vars:
    shop_version: 2.14.1
  tasks:
    - name: Install Docker
      ansible.builtin.apt:
        name: [docker.io, docker-compose-v2]
        state: present
        update_cache: true
    - name: Copy the compose project
      ansible.builtin.template:
        src: compose.yaml.j2
        dest: /srv/shop/compose.yaml
        mode: "0644"
      notify: Restart shop
    - name: Start the shop
      community.docker.docker_compose_v2:
        project_src: /srv/shop
        state: present
  handlers:
    - name: Restart shop
      community.docker.docker_compose_v2:
        project_src: /srv/shop
        state: restarted
`}, nil)

	c.do("PUT", "/api/workspaces/acme/docs/file", map[string]any{"path": "docs/runbooks/deploy.md", "content": "# Deploy the shop\n\n1. Merge to `main`; CI builds `ghcr.io/acme/shop-*`.\n2. On staging: `docker compose pull && docker compose up -d` in `/srv/shop`.\n3. Check `/healthz`, then the same on web-1 and web-2.\n"}, nil)
	// the topology diagram: edge, the prod VPC, the office link
	n := func(id, typ, label, zone, res string, tech ...string) map[string]any {
		m := map[string]any{"id": id, "type": typ, "label": label, "zone": zone, "tech": tech}
		if res != "" {
			m["resource"] = res
		}
		return m
	}
	l := func(from, to, kind, port string, enc bool) map[string]any {
		return map[string]any{"id": from + "-" + to, "from": from, "to": to, "kind": kind, "port": port, "encrypted": enc}
	}
	c.do("PUT", "/api/workspaces/acme/diagram", map[string]any{"rev": "", "topology": map[string]any{
		"zones": []any{
			map[string]any{"id": "edge", "label": "Edge", "kind": "cloud", "provider": "Cloudflare"},
			map[string]any{"id": "vpc", "label": "prod VPC", "kind": "vpc", "cidr": "10.20.0.0/16", "provider": "Hetzner"},
			map[string]any{"id": "office", "label": "Office", "kind": "office"},
		},
		"nodes": []any{
			n("users", "users", "Customers", "", ""),
			n("cdn", "cdn", "Cloudflare", "edge", "", "WAF", "TLS"),
			n("lb", "load-balancer", "HAProxy", "vpc", "", "HAProxy 2.9"),
			n("web-1", "container-host", "web-1", "vpc", "host/web-1", "Docker 27", "shop"),
			n("web-2", "container-host", "web-2", "vpc", "host/web-2", "Docker 27", "shop"),
			n("api-1", "server", "api-1", "vpc", "host/api-1", "Go"),
			n("worker-1", "server", "worker-1", "vpc", "host/worker-1"),
			n("db", "database", "db-1", "vpc", "database/shop-db", "PostgreSQL 16"),
			n("bastion", "bastion", "bastion", "vpc", "host/bastion", "OpenSSH"),
			n("grafana", "monitoring", "Grafana", "vpc", "panel/grafana", "Prometheus"),
			n("vpn", "vpn-gateway", "WireGuard", "office", "network/office", "WireGuard"),
		},
		"links": []any{
			l("users", "cdn", "https", "443", true), l("cdn", "lb", "https", "443", true),
			l("lb", "web-1", "http", "80", false), l("lb", "web-2", "http", "80", false),
			l("web-1", "api-1", "http", "8081", false), l("web-2", "api-1", "http", "8081", false),
			l("api-1", "db", "postgres", "5432", true), l("worker-1", "db", "postgres", "5432", true),
			l("vpn", "bastion", "wireguard", "51820", true), l("bastion", "web-1", "ssh", "22", true),
			l("grafana", "api-1", "http", "9100", false),
		},
	}}, nil)

	if c.err != nil {
		// docs and ansible are nice to have; report and go on
		fmt.Fprintln(os.Stderr, "demo:", c.err)
		c.err = nil
	}
	return demoAgent(srv)
}
