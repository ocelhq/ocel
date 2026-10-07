package session

import (
	"io"
	"os"
	"slices"
	"strings"
	"testing"
)

const (
	standInEnv    = "OCEL_SSH_STAND_IN"
	standInLogEnv = "OCEL_SSH_STAND_IN_LOG"
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
	at := slices.Index(args, "-W")
	if at < 0 {
		return 0
	}
	_, _ = os.Stdout.WriteString(args[at+1] + "\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}
