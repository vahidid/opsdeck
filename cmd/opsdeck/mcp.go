package main

import (
	"encoding/json"
	"flag"
	"os"
	"strconv"

	"opsdeck/internal/server"
)

// flagSet reports whether a flag was given on the command line.
func flagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// mcpEndpoint is where `opsdeck mcp` connects: --url, then OPSDECK_URL, then
// the port given with --port, then the running opsdeck's server.json, then
// the default port.
func mcpEndpoint(url string, port int, portSet bool, stateDir string) string {
	if url != "" {
		return url
	}
	if u := os.Getenv("OPSDECK_URL"); u != "" {
		return u
	}
	if !portSet {
		var info server.ServerInfo
		if b, err := os.ReadFile(server.ServerInfoFile(stateDir)); err == nil && json.Unmarshal(b, &info) == nil && info.MCP != "" {
			return info.MCP
		}
	}
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/mcp/ext"
}
