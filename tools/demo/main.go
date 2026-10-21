// Command demo starts local SSH servers (a bastion and an app host reachable
// through it) and a scripted model endpoint, for trying opsdeck without real
// infrastructure. Usage: go run ./tools/demo -pubkey "ssh-ed25519 AAAA..."
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"golang.org/x/crypto/ssh"

	"opsdeck/internal/testutil/fakellm"
	"opsdeck/internal/testutil/sshd"
)

func main() {
	pubkey := flag.String("pubkey", "", "authorized public key (authorized_keys format)")
	dir := flag.String("dir", os.TempDir(), "working directory for commands")
	flag.Parse()
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(*pubkey))
	if err != nil {
		log.Fatalf("pubkey: %v", err)
	}
	opts := sshd.Options{AuthorizedKeys: []ssh.PublicKey{pub}, Dir: *dir}
	bastion, err := sshd.Start("127.0.0.1:0", opts)
	if err != nil {
		log.Fatal(err)
	}
	app, err := sshd.Start("127.0.0.1:0", opts)
	if err != nil {
		log.Fatal(err)
	}
	llm := fakellm.New([]fakellm.Turn{
		{Text: "I'll look at the inventory and check the app host first.", Tools: []fakellm.ToolCall{
			{Name: "inventory", Args: map[string]any{}},
			{Name: "exec", Args: map[string]any{"target": "app-1", "command": "uname -a && uptime && df -h / | tail -1"}},
		}},
		{Text: "The host is healthy. Now I'll write the marker file the deploy expects.", Tools: []fakellm.ToolCall{
			{Name: "exec", Args: map[string]any{"target": "app-1", "command": "mkdir -p /tmp/opsdeck-demo && echo deployed > /tmp/opsdeck-demo/marker"}},
		}},
		{Text: "## Report\n\n- `app-1` is up (checked uname, uptime, disk).\n- Created `/tmp/opsdeck-demo/marker`.\n\nNothing else pending."},
	})
	out, _ := json.Marshal(map[string]string{"bastion": bastion.Addr, "app": app.Addr, "llm": llm.URL})
	fmt.Println(string(out))
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}
