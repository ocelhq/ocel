package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	liveRoot      = "/run/ocel-proxy"
	upstreamsDir  = "upstreams"
	liveConfig    = "caddy.json"
	flipLock      = ".flip"
	forwardModule = "reverse_proxy"
)

const probeTimeout = 5 * time.Second

type shaped struct {
	dir       string
	config    []byte
	upstreams map[string]string
	probes    map[string]time.Duration
}

func shaping(live string, document []byte) (shaped, error) {
	var config map[string]any
	if err := json.Unmarshal(document, &config); err != nil {
		return shaped{}, fmt.Errorf("is not valid json: %w", err)
	}
	dir := filepath.Join(live, upstreamsDir)
	upstreams := map[string]string{}
	probes := map[string]time.Duration{}
	apps, _ := config["apps"].(map[string]any)
	web, _ := apps["http"].(map[string]any)
	servers, _ := web["servers"].(map[string]any)
	for _, server := range slices.Sorted(maps.Keys(servers)) {
		declared, _ := servers[server].(map[string]any)
		routes, _ := declared["routes"].([]any)
		for _, route := range routes {
			held, _ := route.(map[string]any)
			identity, _ := held["@id"].(string)
			if identity == "" {
				continue
			}
			handlers, _ := held["handle"].([]any)
			for _, handler := range handlers {
				forwards, _ := handler.(map[string]any)
				pool, _ := forwards["upstreams"].([]any)
				if forwards["handler"] != forwardModule || len(pool) != 1 {
					continue
				}
				upstream, _ := pool[0].(map[string]any)
				dial, _ := upstream["dial"].(string)
				named := url.PathEscape(identity)
				switch _, taken := upstreams[named]; {
				case dial == "":
					continue
				case strings.HasPrefix(named, "."):
					return shaped{}, fmt.Errorf("names route %q, which cannot name the file its upstream is read from", identity)
				case taken:
					return shaped{}, fmt.Errorf("forwards route %q to more than one upstream", identity)
				case strings.ContainsAny(dial, "{}"):
					return shaped{}, fmt.Errorf("forwards route %q to %q, a placeholder rather than an address", identity, dial)
				}
				probe, err := probing(forwards)
				if err != nil {
					return shaped{}, fmt.Errorf("probes route %q %w", identity, err)
				}
				upstreams[named] = dial
				probes[named] = probe
				upstream["dial"] = "{file." + filepath.Join(dir, named) + "}"
			}
		}
	}
	written, err := json.Marshal(config)
	if err != nil {
		return shaped{}, err
	}
	return shaped{dir: dir, config: written, upstreams: upstreams, probes: probes}, nil
}

func probing(forwards map[string]any) (time.Duration, error) {
	checks, _ := forwards["health_checks"].(map[string]any)
	active, _ := checks["active"].(map[string]any)
	if active == nil {
		return 0, nil
	}
	switch timeout := active["timeout"].(type) {
	case nil:
		return probeTimeout, nil
	case float64:
		return time.Duration(timeout), nil
	case string:
		read, err := time.ParseDuration(timeout)
		if err != nil {
			return 0, fmt.Errorf("with a timeout %q it cannot read: %w", timeout, err)
		}
		return read, nil
	default:
		return 0, fmt.Errorf("with a timeout %v that is neither a duration nor nanoseconds", timeout)
	}
}

func (s shaped) introduced() error {
	return s.pointed(func(_ string, _ []byte, standing bool) bool { return !standing })
}

func (s shaped) moved() (time.Duration, error) {
	var probed time.Duration
	err := s.pointed(func(named string, held []byte, standing bool) bool {
		if !standing {
			return true
		}
		if string(held) == s.upstreams[named] {
			return false
		}
		probed = max(probed, s.probes[named])
		return true
	})
	return probed, err
}

func (s shaped) pointed(moving func(named string, held []byte, standing bool) bool) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	for _, named := range slices.Sorted(maps.Keys(s.upstreams)) {
		held, err := os.ReadFile(filepath.Join(s.dir, named))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if !moving(named, held, err == nil) {
			continue
		}
		if err := s.pointing(named); err != nil {
			return err
		}
	}
	return nil
}

func (s shaped) pointing(named string) error {
	staged, err := os.CreateTemp(s.dir, "."+named+".")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	if _, err := staged.WriteString(s.upstreams[named]); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	return os.Rename(staged.Name(), filepath.Join(s.dir, named))
}

func (s shaped) pruned() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, kept := s.upstreams[entry.Name()]; kept || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func holding(live string) (func(), error) {
	if err := os.MkdirAll(live, 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(live, flipLock), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, err
	}
	return func() { lock.Close() }, nil
}
