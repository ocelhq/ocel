package enginetest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const (
	hostPortPoll         = 200 * time.Millisecond
	hostPortBeforeKilled = 15 * time.Second
)

func HostPorts(t *testing.T, ports ...string) {
	t.Helper()
	dir, err := slotDir()
	if err != nil {
		t.Fatalf("no directory to hold this machine's host ports in: %v", err)
	}
	hostPortsIn(t, dir, ports)
}

func hostPortsIn(t *testing.T, dir string, ports []string) {
	t.Helper()
	var giveUp time.Time
	if deadline, bounded := t.Deadline(); bounded {
		giveUp = deadline.Add(-hostPortBeforeKilled)
	}
	where, _ := os.Getwd()
	holder := fmt.Sprintf("%s in %s (process %s)", t.Name(), where, run)
	release, err := holdHostPorts(dir, holder, ports, giveUp, func(port, other string) {
		t.Logf("waiting for host port %s, which %s holds", port, other)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
}

func holdHostPorts(dir, holder string, ports []string, giveUp time.Time, waiting func(port, other string)) (release func(), err error) {
	var held []*os.File
	release = func() {
		for _, lock := range held {
			lock.Close()
		}
	}
	for _, port := range slices.Compact(slices.Sorted(slices.Values(ports))) {
		lock, err := holdHostPort(filepath.Join(dir, "port-"+port+".lock"), port, holder, giveUp, waiting)
		if err != nil {
			release()
			return nil, err
		}
		held = append(held, lock)
	}
	return release, nil
}

func holdHostPort(at, port, holder string, giveUp time.Time, waiting func(port, other string)) (*os.File, error) {
	told := false
	for {
		held, free, err := lockIfFree(at)
		if err != nil {
			return nil, fmt.Errorf("lock host port %s: %w", port, err)
		}
		if free {
			if held.Truncate(0) == nil {
				_, _ = held.WriteAt([]byte(holder), 0)
			}
			return held, nil
		}
		other := holderOf(at)
		if !giveUp.IsZero() && time.Now().After(giveUp) {
			return nil, fmt.Errorf("host port %s is still held by %s: let that run finish, or run with a longer -timeout", port, other)
		}
		if !told {
			waiting(port, other)
			told = true
		}
		time.Sleep(hostPortPoll)
	}
}

func holderOf(at string) string {
	said, err := os.ReadFile(at)
	if err != nil || len(bytes.TrimSpace(said)) == 0 {
		return "another run"
	}
	return string(said)
}
