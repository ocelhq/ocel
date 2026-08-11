//go:build !windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/channel"
)

func TestListen(t *testing.T) {
	t.Run("binds unix socket", func(t *testing.T) {
		ln, addr, err := listen()
		if err != nil {
			t.Fatalf("listen() error = %v", err)
		}
		defer ln.Close()

		if !strings.HasPrefix(addr, "unix:") {
			t.Fatalf("listen() addr = %q, want unix: scheme", addr)
		}

		network, path, err := channel.ParseAddr(addr)
		if err != nil {
			t.Fatalf("ParseAddr(%q) error = %v", addr, err)
		}
		if network != "unix" {
			t.Fatalf("ParseAddr(%q) network = %q, want unix", addr, network)
		}

		if _, err := os.Stat(path); err != nil {
			t.Fatalf("socket file %q not present: %v", path, err)
		}
	})

	t.Run("closing removes socket file", func(t *testing.T) {
		ln, addr, err := listen()
		if err != nil {
			t.Fatalf("listen() error = %v", err)
		}
		_, path, err := channel.ParseAddr(addr)
		if err != nil {
			t.Fatalf("ParseAddr(%q) error = %v", addr, err)
		}

		if err := ln.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}

		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("socket file %q still present after Close(): err = %v", path, err)
		}
	})

	t.Run("unique paths across calls", func(t *testing.T) {
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
	})
}
