package ssh

import (
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	standInEnv    = "OCEL_SSH_STAND_IN"
	standInLogEnv = "OCEL_SSH_STAND_IN_LOG"
	standInRunEnv = "OCEL_SSH_STAND_IN_RUN"
	standInOpEnv  = "OCEL_SSH_STAND_IN_REFUSE"
)

func TestMain(m *testing.M) {
	if os.Getenv(standInEnv) != "" {
		os.Exit(standInForSSH(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func standInForSSH(args []string) int {
	if log := os.Getenv(standInLogEnv); log != "" {
		said, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return 1
		}
		_, _ = said.WriteString(strings.Join(args, " ") + "\n")
		_ = said.Close()
	}
	if op := slices.Index(args, "-O"); op >= 0 {
		if refused := os.Getenv(standInOpEnv); refused != "" && args[op+1] == refused {
			_, _ = os.Stderr.WriteString("mux_client_forward: forwarding request failed: remote port forwarding failed\n")
			return 255
		}
		return 0
	}
	at := slices.Index(args, "-W")
	if at < 0 && os.Getenv(standInRunEnv) != "" && len(args) > 0 {
		shell := exec.Command("/bin/sh", "-c", args[len(args)-1])
		shell.Stdin, shell.Stdout, shell.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := shell.Run(); err != nil {
			return 1
		}
		return 0
	}
	if at < 0 {
		return 0
	}
	_, _ = os.Stdout.WriteString(args[at+1] + "\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}
