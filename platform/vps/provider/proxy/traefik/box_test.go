package traefik_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type box struct {
	mu         sync.Mutex
	asked      []string
	beside     []switchboard.Neighbour
	containers string
	services   string
	coolify    string
	spec       proxy.Spec
	routes     map[string][]string
	failures   map[string]string
	paused     []time.Duration
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
	case strings.Contains(command, "coolify-proxy"):
		return b.coolify, nil
	default:
		return "", nil
	}
}

func (b *box) Spec(context.Context) (proxy.Spec, error) { return b.spec, nil }

func (b *box) Routed(_ context.Context, hostname string) (string, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked = append(b.asked, "routed "+hostname)
	answers := b.routes[hostname]
	if len(answers) == 0 {
		return "", b.failures[hostname], nil
	}
	answered := answers[0]
	if len(answers) > 1 {
		b.routes[hostname] = answers[1:]
	}
	if answered == "" {
		return "", b.failures[hostname], nil
	}
	return answered, "", nil
}

func (b *box) Pause(_ context.Context, wait time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused = append(b.paused, wait)
	return nil
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
