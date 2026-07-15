package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// controlPair gives a test a listener on a real unix socket plus a dial
// function standing in for the node child connecting back to it.
func controlPair(t *testing.T) (net.Listener, func() net.Conn) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln, func() net.Conn {
		c, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
}

func TestAwaitReady_ReturnsPortOnServerReady(t *testing.T) {
	ln, dial := controlPair(t)
	go func() {
		c := dial()
		fmt.Fprintln(c, `{"type":"log","payload":{"message":"booting"}}`)
		fmt.Fprintln(c, `{"type":"server-ready","payload":{"httpPort":41234}}`)
	}()

	ready, err := awaitReady(ln, make(chan error, 1), time.Second)
	if err != nil {
		t.Fatalf("awaitReady() error = %v, want nil", err)
	}
	if ready.httpPort != 41234 {
		t.Errorf("httpPort = %d, want 41234", ready.httpPort)
	}
}

// The common failure: the user's app throws on import, so node dies before it
// ever connects. Waiting out the budget would burn the sandbox's init for
// nothing — the exit is the answer, and it must arrive as soon as it happens.
func TestAwaitReady_AbortsImmediatelyWhenNodeExitsBeforeConnecting(t *testing.T) {
	ln, _ := controlPair(t)
	exited := make(chan error, 1)
	exited <- errors.New("exit status 1")

	start := time.Now()
	_, err := awaitReady(ln, exited, 30*time.Second)
	if err == nil {
		t.Fatal("awaitReady() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("error = %q, want it to carry the child's exit status", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s, want an immediate abort rather than waiting out the budget", elapsed)
	}
}

// The reaper is what turns a crashed child into an immediate error rather than
// a stall, so exercise it against a real process exiting without ever
// connecting — the shape of an app that throws on import — wired exactly as
// startNode wires it.
func TestAwaitReady_ReapsARealChildThatDiesWithoutConnecting(t *testing.T) {
	ln, _ := controlPair(t)
	cmd := exec.Command("sh", "-c", "exit 1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	start := time.Now()
	_, err := awaitReady(ln, exited, 30*time.Second)
	if err == nil {
		t.Fatal("awaitReady() error = nil, want the child's exit reported")
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("error = %q, want it to carry the real child's exit status", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s, want the reaper to abort as soon as the child died", elapsed)
	}
}

func TestAwaitReady_FailsWhenNodeNeverSignalsReadyWithinBudget(t *testing.T) {
	ln, dial := controlPair(t)
	go func() {
		c := dial()
		fmt.Fprintln(c, `{"type":"log","payload":{"message":"still working"}}`)
		select {} // connected, alive, never ready
	}()

	_, err := awaitReady(ln, make(chan error, 1), 100*time.Millisecond)
	if err == nil {
		t.Fatal("awaitReady() error = nil, want a budget-expiry error")
	}
	if !strings.Contains(err.Error(), "100ms") {
		t.Errorf("error = %q, want it to name the budget that expired", err)
	}
}

// A stack trace node managed to report before dying is the whole diagnosis;
// the handshake must not drop it on the floor in favour of a bare exit status.
func TestAwaitReady_CarriesTheLastLogIntoTheError(t *testing.T) {
	ln, dial := controlPair(t)
	exited := make(chan error, 1)
	go func() {
		c := dial()
		fmt.Fprintln(c, `{"type":"log","payload":{"message":"SyntaxError: unexpected token"}}`)
		time.Sleep(50 * time.Millisecond)
		exited <- errors.New("exit status 1")
	}()

	_, err := awaitReady(ln, exited, 5*time.Second)
	if err == nil {
		t.Fatal("awaitReady() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "SyntaxError: unexpected token") {
		t.Errorf("error = %q, want it to carry the last log node reported", err)
	}
}

func TestEntrypointPath(t *testing.T) {
	const nodeEntry = "/opt/ocel/node/entrypoint.mjs"
	const nextEntry = "/opt/ocel/next/entrypoint.mjs"

	cases := []struct {
		name   string
		config string // "" means no config.json written
		want   string
	}{
		{"next framework", `{"framework":"next"}`, nextEntry},
		{"node framework", `{"framework":"node"}`, nodeEntry},
		{"empty framework", `{"framework":""}`, nodeEntry},
		{"no config file", "", nodeEntry},
		{"invalid json", `{not json`, nodeEntry},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.config != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("LAMBDA_TASK_ROOT", dir)
			if got := entrypointPath(); got != tc.want {
				t.Errorf("entrypointPath() = %q, want %q", got, tc.want)
			}
		})
	}
}
