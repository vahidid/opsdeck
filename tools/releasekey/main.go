// Command releasekey makes and uses the key that signs opsdeck releases
// (docs/releasing.md):
//
//	go run ./tools/releasekey gen                  new key pair
//	OPSDECK_SIGNING_KEY=… go run ./tools/releasekey sign FILE [COMMENT]
//	go run ./tools/releasekey verify FILE          check FILE.minisig with the built-in keys
//	go run ./tools/releasekey pub                  print the current release key
package main

import (
	"errors"
	"fmt"
	"os"

	"opsdeck/internal/minisign"
	"opsdeck/internal/update"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "releasekey:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: releasekey gen | sign FILE [COMMENT] | verify FILE | pub")
	}
	switch args[0] {
	case "gen":
		k, err := minisign.Generate()
		if err != nil {
			return err
		}
		fmt.Printf("Public key (add it to internal/update/keys.go and docs/releasing.md):\n  %s\n\n", k.Public().String())
		fmt.Printf("Signing key (store it as the GitHub Actions secret OPSDECK_SIGNING_KEY, nowhere else):\n  %s\n", k.Encode())
		return nil
	case "sign":
		if len(args) < 2 {
			return errors.New("usage: releasekey sign FILE [COMMENT]")
		}
		k, err := minisign.ParsePrivateKey(os.Getenv("OPSDECK_SIGNING_KEY"))
		if err != nil {
			return fmt.Errorf("OPSDECK_SIGNING_KEY: %w", err)
		}
		if !update.TrustsKey(k.Public()) {
			return errors.New("this signing key is not one of the release keys in internal/update/keys.go: the updater would refuse its signatures")
		}
		msg, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		comment := "opsdeck release"
		if len(args) > 2 {
			comment = args[2]
		}
		return os.WriteFile(args[1]+".minisig", minisign.Sign(k, msg, comment), 0o644)
	case "pub":
		fmt.Println(update.ReleaseKeys[len(update.ReleaseKeys)-1])
		return nil
	case "verify":
		if len(args) < 2 {
			return errors.New("usage: releasekey verify FILE")
		}
		msg, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		sig, err := os.ReadFile(args[1] + ".minisig")
		if err != nil {
			return err
		}
		tc, err := update.VerifySums(msg, sig)
		if err != nil {
			return err
		}
		fmt.Println("good signature:", tc)
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}
