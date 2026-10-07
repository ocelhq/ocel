package session

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const standInEnv = "OCEL_SSH_STAND_IN"

func TestHelperSSHStandIn(t *testing.T) {
	if os.Getenv(standInEnv) == "" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if slices.Contains(args, "-O") {
		os.Exit(0)
	}
	spec := args[slices.Index(args, "-L")+1]
	parts := strings.Split(spec, ":")
	listener, err := net.Listen("tcp", parts[0]+":"+parts[1])
	if err != nil || os.Getenv(standInEnv) == "refuse" {
		os.Stderr.WriteString("bind [" + parts[0] + "]:" + parts[1] + ": Address already in use\n")
		os.Exit(255)
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
	os.Exit(0)
}

func sshStandingIn(t *testing.T) *Session {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in ssh is a posix shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" + standInEnv + "=${" + standInEnv + ":-1} exec " + os.Args[0] + " -test.run='^TestHelperSSHStandIn$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &Session{target: Target{Host: "203.0.113.10", User: "ubuntu"}, dest: Destination{User: "ubuntu", Written: "203.0.113.10"}}
}

func readForwarded(t *testing.T, local string) (string, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", local, time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	said, err := bufio.NewReader(conn).ReadString('\n')
	return strings.TrimSpace(said), err
}

func TestAForwardedPortListensOnLoopbackAndReachesTheRemoteAddressUntilItsContextEnds(t *testing.T) {
	session := sshStandingIn(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	local, err := session.ForwardPort(ctx, "172.18.0.5:5432")
	if err != nil {
		t.Fatalf("ForwardPort() error = %v", err)
	}
	if host, _, _ := net.SplitHostPort(local); host != "127.0.0.1" {
		t.Errorf("ForwardPort() listens on %s, want loopback alone: a forward is never reachable from another machine", local)
	}
	if reached, err := readForwarded(t, local); err != nil || reached != "172.18.0.5:5432" {
		t.Fatalf("a connection to %s reached %q, %v, want the remote address 172.18.0.5:5432", local, reached, err)
	}

	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := readForwarded(t, local); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the forward still answered after its context ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAForwardSshCannotOpenIsRefusedWithWhatSshSaid(t *testing.T) {
	session := sshStandingIn(t)
	t.Setenv(standInEnv, "refuse")

	_, err := session.ForwardPort(context.Background(), "172.18.0.5:5432")
	if err == nil || !strings.Contains(err.Error(), "Address already in use") {
		t.Errorf("ForwardPort() over a port already taken = %v, want a refusal saying what ssh said", err)
	}
}
