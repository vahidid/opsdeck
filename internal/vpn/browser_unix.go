//go:build !windows

package vpn

import (
	"bufio"
	"os"
	"os/exec"

	"opsdeck/internal/sysproc"
)

func startBrowser(exe string, args []string, profileDir string) (*exec.Cmd, cdpTransport, error) {
	// fd 3: Chrome reads commands; fd 4: Chrome writes replies and events.
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, nil, err
	}
	cmd := sysproc.Command(exe, append(args, "--remote-debugging-pipe", "about:blank")...)
	cmd.ExtraFiles = []*os.File{inR, outW}
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, nil, err
	}
	inR.Close()
	outW.Close()
	return cmd, &pipeTransport{w: inW, r: bufio.NewReaderSize(outR, 1<<20), rc: outR}, nil
}
