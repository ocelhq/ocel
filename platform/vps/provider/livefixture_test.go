package vps_test

import (
	"archive/tar"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fixtureBase = "ocel-live-app:base"
	fixtureRepo = "ocel-live-app"
)

const fixtureSource = `package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	switch os.Getenv("MODE") {
	case "hang":
		select {}
	case "crash":
		os.Exit(3)
	}
	release := os.Getenv("RELEASE")
	health, err := strconv.Atoi(os.Getenv("HEALTH_STATUS"))
	if err != nil {
		health = http.StatusOK
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(health)
		_, _ = io.WriteString(w, release)
	})
	mux.HandleFunc("/hold", func(w http.ResponseWriter, r *http.Request) {
		held, _ := strconv.Atoi(r.URL.Query().Get("s"))
		time.Sleep(time.Duration(held) * time.Second)
		_, _ = io.WriteString(w, release)
	})
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", release)
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, _ *http.Request) {
		hijacker, held := w.(http.Hijacker)
		if !held {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		for {
			_, _ = io.WriteString(conn, "\x00tick\xff")
			time.Sleep(time.Second)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, release)
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		panic(err)
	}
	panic(http.Serve(listener, mux))
}
`

func (vm machine) feeds(t *testing.T, command string, stdin []byte) string {
	t.Helper()
	run := exec.Command("ssh",
		"-F", vm.config, "-i", vm.key,
		"-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
		vm.user+"@"+vm.addr, command)
	run.Stdin = bytes.NewReader(stdin)
	var errs strings.Builder
	run.Stderr = &errs
	said, err := run.Output()
	if err != nil {
		t.Fatalf("ssh %q: %v\n%s", command, err, errs.String())
	}
	return string(said)
}

func (vm machine) arch(t *testing.T) string {
	t.Helper()
	switch reported := strings.TrimSpace(vm.ssh(t, "uname -m")); reported {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		t.Skipf("no fixture image is built for a box reporting %q", reported)
		return ""
	}
}

func fixtureBinary(t *testing.T, arch string) []byte {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(fixtureSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module ocelfixture\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(dir, "app")
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", built, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch, "GOFLAGS=")
	if said, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the fixture app: %v\n%s", err, said)
	}
	read, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	written := tar.NewWriter(&raw)
	if err := written.WriteHeader(&tar.Header{Name: "app", Mode: 0o755, Size: int64(len(read))}); err != nil {
		t.Fatal(err)
	}
	if _, err := written.Write(read); err != nil {
		t.Fatal(err)
	}
	if err := written.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func fixtures(t *testing.T, vm machine) {
	t.Helper()
	if strings.TrimSpace(vm.ssh(t, "sudo docker image inspect "+fixtureBase+" >/dev/null 2>&1 && echo held || echo gone")) != "held" {
		vm.feeds(t, "sudo docker import --change 'ENTRYPOINT [\"/app\"]' - "+fixtureBase+" >/dev/null",
			fixtureBinary(t, vm.arch(t)))
	}
	for tag, envs := range map[string][]string{
		"one":     {"RELEASE=one"},
		"two":     {"RELEASE=two"},
		"sick":    {"RELEASE=sick", "HEALTH_STATUS=404"},
		"hung":    {"MODE=hang"},
		"crasher": {"MODE=crash"},
	} {
		if strings.TrimSpace(vm.ssh(t, "sudo docker image inspect "+fixtureAt(tag)+" >/dev/null 2>&1 && echo held || echo gone")) == "held" {
			continue
		}
		file := "FROM " + fixtureBase + "\n"
		for _, env := range envs {
			file += "ENV " + env + "\n"
		}
		vm.feeds(t, "sudo docker build -q -t "+fixtureAt(tag)+" - >/dev/null", []byte(file))
	}
}

func fixtureAt(tag string) string { return fixtureRepo + ":" + tag }
