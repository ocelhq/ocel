package childprocesstest

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func WorkerTree(t *testing.T, root, name string) (command []string, startedPath, pidPath string) {
	t.Helper()
	skipWithoutPOSIXShell(t)
	startedPath = filepath.Join(root, name+".started")
	pidPath = filepath.Join(root, name+".workerpid")
	command = []string{"sh", "-c", "sleep 30 & echo $! > " + pidPath + "; touch " + startedPath + "; wait"}
	return command, startedPath, pidPath
}

func DeepWorkerTree(t *testing.T, root, name string) (command []string, startedPath, leafPidPath string) {
	t.Helper()
	skipWithoutPOSIXShell(t)
	scriptPath := filepath.Join(root, name+".sh")
	startedPath = filepath.Join(root, name+".started")
	pidPrefix := filepath.Join(root, name+".workerpid.")
	if err := os.WriteFile(scriptPath, []byte(`#!/bin/sh
depth="$1"
started="$2"
pidprefix="$3"
trap '' INT
echo $$ > "${pidprefix}${depth}"
if [ "$depth" -ge 3 ]; then
  touch "$started"
  exec sleep 30
fi
next=$((depth + 1))
sh "$0" "$next" "$started" "$pidprefix" &
child=$!
wait "$child"
`), 0o644); err != nil {
		t.Fatalf("write %s: %v", scriptPath, err)
	}
	command = []string{"sh", scriptPath, "1", startedPath, pidPrefix}
	return command, startedPath, pidPrefix + "3"
}

func WaitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%q never appeared", path)
}

func WaitDead(t *testing.T, pidPath string) {
	t.Helper()
	WaitForFile(t, pidPath)
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse worker pid %q: %v", raw, err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !IsAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("worker pid %d is still alive after the CLI exited", pid)
}

func skipWithoutPOSIXShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}
}
