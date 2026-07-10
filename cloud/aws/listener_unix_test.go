//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
)

func TestListen_BindsUnixSocket(t *testing.T) {
	ln, addr, err := listen()
	if err != nil {
		t.Fatalf("listen() error = %v", err)
	}
	defer ln.Close()

	if !strings.HasPrefix(addr, "unix:") {
		t.Fatalf("listen() addr = %q, want unix: scheme", addr)
	}

	network, path, err := providerv1.ParseAddr(addr)
	if err != nil {
		t.Fatalf("ParseAddr(%q) error = %v", addr, err)
	}
	if network != "unix" {
		t.Fatalf("ParseAddr(%q) network = %q, want unix", addr, network)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket file %q not present: %v", path, err)
	}
}

func TestListen_ClosingRemovesSocketFile(t *testing.T) {
	ln, addr, err := listen()
	if err != nil {
		t.Fatalf("listen() error = %v", err)
	}
	_, path, err := providerv1.ParseAddr(addr)
	if err != nil {
		t.Fatalf("ParseAddr(%q) error = %v", addr, err)
	}

	if err := ln.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket file %q still present after Close(): err = %v", path, err)
	}
}

func TestListen_UniquePathsAcrossCalls(t *testing.T) {
	ln1, addr1, err := listen()
	if err != nil {
		t.Fatalf("listen() error = %v", err)
	}
	defer ln1.Close()

	ln2, addr2, err := listen()
	if err != nil {
		t.Fatalf("listen() error = %v", err)
	}
	defer ln2.Close()

	if addr1 == addr2 {
		t.Fatalf("two listen() calls returned the same address %q", addr1)
	}
}
