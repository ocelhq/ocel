package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
