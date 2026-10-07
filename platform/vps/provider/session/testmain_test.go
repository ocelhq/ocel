package session

import (
	"bufio"
	"net"
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
	if slices.Contains(args, "-O") {
		if log := os.Getenv(standInLogEnv); log != "" {
			said, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return 1
			}
			defer said.Close()
			_, _ = said.WriteString(strings.Join(args, " ") + "\n")
		}
		return 0
	}
	spec := args[slices.Index(args, "-L")+1]
	parts := strings.Split(spec, ":")
	listener, err := net.Listen("tcp", parts[0]+":"+parts[1])
	if err != nil || os.Getenv(standInEnv) == "refuse" || refusesFirst() {
		os.Stderr.WriteString("bind [" + parts[0] + "]:" + parts[1] + ": Address already in use\n")
		return 255
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte(parts[2] + ":" + parts[3] + "\n"))
			_ = conn.Close()
		}
	}()
	_, _ = bufio.NewReader(os.Stdin).ReadString(0)
	return 0
}

func refusesFirst() bool {
	if os.Getenv(standInEnv) != "refuse-first" {
		return false
	}
	marker, err := os.OpenFile(os.Getenv(standInLogEnv), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	_ = marker.Close()
	return true
}
