//go:build unix

package main

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestAKeysFileSlowToReadHoldsUpNoOtherLookup(t *testing.T) {
	t.Parallel()

	app := newKey(t)
	path := filepath.Join(t.TempDir(), "keys.json")
	writeKeysFile(t, path, map[string]ed25519.PrivateKey{"app": app})
	clock := &fakeClock{}
	keys, err := newKeysFileSet(path, clock.now)
	if err != nil {
		t.Fatalf("newKeysFileSet() = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	clock.advance(keysFileMaxAge)

	found := make(chan bool, 2)
	for range 2 {
		go func() {
			_, known := keys.find("app")
			found <- known
		}()
	}
	select {
	case known := <-found:
		if !known {
			t.Error("a lookup made while the keys file was being read again lost the key read before")
		}
	case <-time.After(5 * time.Second):
		t.Error("both lookups waited on one read of the keys file, and every token check waits as long as Secret Manager's volume takes to answer")
	}

	writer, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString(keysOf(t, map[string]ed25519.PrivateKey{"app": app})); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	select {
	case <-found:
	case <-time.After(5 * time.Second):
		t.Error("the lookup reading the keys file never returned once the file was written")
	}
}
