package session

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sshStandingIn(t *testing.T) *Session {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in ssh is a posix shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" + standInEnv + "=${" + standInEnv + ":-1} exec " + os.Args[0] + " \"$@\"\n"
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

	local, _, err := session.ForwardPort(ctx, "172.18.0.5:5432")
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

func TestAForwardsPortIsHeldByTheProviderFromBeforeItIsHandedBackSoNoOtherProcessCanTakeIt(t *testing.T) {
	session := sshStandingIn(t)
	log := filepath.Join(t.TempDir(), "said")
	t.Setenv(standInLogEnv, log)

	local, stop, err := session.ForwardPort(context.Background(), "172.18.0.5:5432")
	if err != nil {
		t.Fatalf("ForwardPort() error = %v", err)
	}
	defer stop()

	if said, _ := os.ReadFile(log); len(said) > 0 {
		t.Errorf("ssh ran as %q before anything connected, want the port held by the provider itself: a port ssh binds after the provider let it go can be taken first, and handed the database's password", said)
	}
	if squatter, err := net.Listen("tcp", local); err == nil {
		squatter.Close()
		t.Fatalf("another listener took %s while the forward held it", local)
	}
	if reached, err := readForwarded(t, local); err != nil || reached != "172.18.0.5:5432" {
		t.Errorf("a connection to %s reached %q, %v, want the remote address 172.18.0.5:5432", local, reached, err)
	}
}

func TestStoppingAForwardCutsEveryConnectionItCarriesAndStopsListening(t *testing.T) {
	session := sshStandingIn(t)

	local, stop, err := session.ForwardPort(context.Background(), "172.18.0.5:5432")
	if err != nil {
		t.Fatalf("ForwardPort() error = %v", err)
	}
	conn, err := net.DialTimeout("tcp", local, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("the forward carried nothing: %v", err)
	}

	stop()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := reader.ReadByte(); err == nil || os.IsTimeout(err) {
		t.Errorf("a connection the forward carried read %v once it stopped, want it cut before stop returns", err)
	}
	if _, err := readForwarded(t, local); err == nil {
		t.Error("the forward still answered once stopped")
	}
}
