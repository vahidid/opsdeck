package update

import (
	"os"
	"strings"
	"testing"
)

// The install script checks signatures with the current release key (the
// tests swap ReleaseKeys for a test key).
func TestInstallScriptKey(t *testing.T) {
	b, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "RELEASE_KEY="+releaseKey2026+"\n") {
		t.Fatal("scripts/install.sh: RELEASE_KEY is not the current release key")
	}
}
