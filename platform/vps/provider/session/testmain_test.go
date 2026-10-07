package session

import (
	"bufio"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
)

const standInEnv = "OCEL_SSH_STAND_IN"

func TestMain(m *testing.M) {
	if os.Getenv(standInEnv) != "" {
		os.Exit(standInForSSH(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func standInForSSH(args []string) int {
	if slices.Contains(args, "-O") {
		return 0
	}
	spec := args[slices.Index(args, "-L")+1]
	parts := strings.Split(spec, ":")
	listener, err := net.Listen("tcp", parts[0]+":"+parts[1])
	if err != nil || os.Getenv(standInEnv) == "refuse" {
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
