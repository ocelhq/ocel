package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAPullAsWithAnArgumentDockerWouldReadAsAnOptionRunsNothing(t *testing.T) {
	t.Parallel()

	ran := filepath.Join(t.TempDir(), "ran")
	docker := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\ntouch "+ran+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	errs, err := os.CreateTemp(t.TempDir(), "errs")
	if err != nil {
		t.Fatal(err)
	}
	defer errs.Close()
	for _, argv := range [][]string{
		{"--config=/var/lib/ocel/evil", "ada", "ghcr.io/shop/web@sha256:0"},
		{"ghcr.io", "-H", "ghcr.io/shop/web@sha256:0"},
		{"ghcr.io", "ada", "--privileged"},
		{"ghcr.io", "ada"},
	} {
		if code := pullAs(context.Background(), docker, argv, errs); code != refusedExit {
			t.Errorf("pull-as %q exited %d, want it refused", argv, code)
		}
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("a refused pull-as ran docker anyway")
	}
}
