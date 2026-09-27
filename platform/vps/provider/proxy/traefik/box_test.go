package traefik_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type box struct {
	mu         sync.Mutex
	asked      []string
	beside     []switchboard.Neighbour
	containers string
	services   string
}

func (b *box) Ran(_ context.Context, _ string, argv []string) (string, error) {
	command := strings.Join(argv, " ")
	b.mu.Lock()
	b.asked = append(b.asked, command)
	b.mu.Unlock()
	switch {
	case strings.Contains(command, "docker service"):
		return b.services, nil
	case strings.Contains(command, "docker ps"):
		return b.containers, nil
	default:
		return "", nil
	}
}

func (b *box) Beside(_ context.Context, path string) ([]switchboard.Neighbour, error) {
	b.mu.Lock()
	b.asked = append(b.asked, "beside "+path)
	b.mu.Unlock()
	return b.beside, nil
}

func fixture(t *testing.T, path string) string {
	t.Helper()
	read, err := os.ReadFile(filepath.Join("testdata", path))
	if err != nil {
		t.Fatal(err)
	}
	return string(read)
}

func besideIn(t *testing.T, dir string) []switchboard.Neighbour {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("testdata", dir))
	if err != nil {
		t.Fatal(err)
	}
	var found []switchboard.Neighbour
	for _, entry := range entries {
		if ext := filepath.Ext(entry.Name()); ext != ".yml" && ext != ".yaml" && ext != ".toml" {
			continue
		}
		found = append(found, switchboard.Neighbour{Name: entry.Name(), Content: []byte(fixture(t, filepath.Join(dir, entry.Name())))})
	}
	return found
}
