package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/runtimekit/originguard"
)

const (
	roleVar    = "OCEL_TEST_CONTAINER_ROLE"
	healthPath = "/_ocel/health"
	lingerFor  = 400 * time.Millisecond
)

type served struct {
	Port   string `json:"port"`
	Secret string `json:"secret"`
	Health string `json:"health"`
}

func TestContainerHelper(t *testing.T) {
	role := os.Getenv(roleVar)
	if role == "" {
		t.Skip("this test is the app the container runtime tests run")
	}
	go func() {
		time.Sleep(60 * time.Second)
		os.Exit(99)
	}()

	if role == "linger" {
		time.Sleep(lingerFor)
		os.Exit(0)
	}
	if strings.HasPrefix(role, "exit:") {
		code, err := strconv.Atoi(strings.TrimPrefix(role, "exit:"))
		if err != nil {
			os.Exit(98)
		}
		os.Exit(code)
	}

	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	go func() {
		<-term
		os.Exit(0)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/quit", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		go func() {
			time.Sleep(50 * time.Millisecond)
			os.Exit(0)
		}()
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(served{
			Port:   os.Getenv(providerkit.InjectedPortName),
			Secret: os.Getenv(originguard.OriginSecretVar),
			Health: os.Getenv(originguard.HealthPathVar),
		})
	})
	if err := http.ListenAndServe("127.0.0.1:"+os.Getenv(providerkit.InjectedPortName), mux); err != nil {
		os.Exit(97)
	}
}

func appCommand(t *testing.T) []string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{binary, "-test.run=^TestContainerHelper$"}
}

func exposedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

type running struct {
	port string
	code chan int
}

func launch(t *testing.T, role string, environ ...string) *running {
	t.Helper()
	port := exposedPort(t)
	command := appCommand(t)
	environ = append([]string{providerkit.InjectedPortName + "=" + port}, environ...)

	out := &running{port: port, code: make(chan int, 1)}
	go func() { out.code <- run(context.Background(), command, append(environ, roleVar+"="+role)) }()
	return out
}

func (r *running) ask(t *testing.T, path, secret string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+r.port+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		req.Header.Set(originguard.OriginSecretHeader, secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("the front did not answer %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (r *running) awaitFront(t *testing.T, secret string) served {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case code := <-r.code:
			t.Fatalf("the runtime exited with %d before it served anything", code)
		default:
		}
		req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+r.port+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		if secret != "" {
			req.Header.Set(originguard.OriginSecretHeader, secret)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			time.Sleep(20 * time.Millisecond)
			continue
		}
		var body served
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("the app answered something the test cannot read: %v", err)
		}
		return body
	}
	t.Fatal("the front never served the app")
	return served{}
}

func (r *running) quit(t *testing.T, secret string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+r.port+"/quit", nil)
	if secret != "" {
		req.Header.Set(originguard.OriginSecretHeader, secret)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
	return r.await(t)
}

func (r *running) await(t *testing.T) int {
	t.Helper()
	select {
	case code := <-r.code:
		return code
	case <-time.After(30 * time.Second):
		t.Fatal("the runtime never returned")
		return 0
	}
}

func TestRun(t *testing.T) {
	t.Run("refuses an image that names no command", func(t *testing.T) {
		if code := run(context.Background(), nil, nil); code != 1 {
			t.Errorf("run = %d, want 1 for an image with no entrypoint", code)
		}
	})

	t.Run("serves the app through the exposed port and hands it a port of its own", func(t *testing.T) {
		r := launch(t, "serve")
		body := r.awaitFront(t, "")

		if body.Port == "" {
			t.Fatal("the app was handed no port to bind at all")
		}
		if body.Port == r.port {
			t.Errorf("the app was handed %s=%s, the very port the platform routes to; the front must own the exposed port", providerkit.InjectedPortName, body.Port)
		}
		if _, err := strconv.Atoi(body.Port); err != nil {
			t.Errorf("the app was handed %s=%q, which is no port", providerkit.InjectedPortName, body.Port)
		}
		if code := r.quit(t, ""); code != 0 {
			t.Errorf("run = %d, want 0 for an app that finished cleanly", code)
		}
	})

	t.Run("the origin secret guards the front and never reaches the app", func(t *testing.T) {
		const secret = "s3cr3t"
		r := launch(t, "serve", originguard.OriginSecretVar+"="+secret)
		body := r.awaitFront(t, secret)

		if body.Secret != "" {
			t.Errorf("the app was handed %s=%q; nothing the app runs or logs may carry the origin secret", originguard.OriginSecretVar, body.Secret)
		}
		if resp := r.ask(t, "/", ""); resp.StatusCode != http.StatusForbidden {
			t.Errorf("status without the secret = %d, want %d: a caller who came round the edge is not the app's to answer", resp.StatusCode, http.StatusForbidden)
		}
		if resp := r.ask(t, "/", "wrong"); resp.StatusCode != http.StatusForbidden {
			t.Errorf("status with the wrong secret = %d, want %d", resp.StatusCode, http.StatusForbidden)
		}
		if code := r.quit(t, secret); code != 0 {
			t.Errorf("run = %d, want 0", code)
		}
	})

	t.Run("the platform's probe is answered without the secret", func(t *testing.T) {
		const secret = "s3cr3t"
		r := launch(t, "serve", originguard.OriginSecretVar+"="+secret, originguard.HealthPathVar+"="+healthPath)
		body := r.awaitFront(t, secret)

		if body.Health != healthPath {
			t.Errorf("the app was handed %s=%q, want %q: the app serves the path the platform probes", originguard.HealthPathVar, body.Health, healthPath)
		}
		if resp := r.ask(t, healthPath, ""); resp.StatusCode != http.StatusOK {
			t.Errorf("probe status = %d, want %d: the platform's probe carries no secret and its failure takes the deployment down", resp.StatusCode, http.StatusOK)
		}
		if code := r.quit(t, secret); code != 0 {
			t.Errorf("run = %d, want 0", code)
		}
	})

	t.Run("the exposed port opens only once the app listens", func(t *testing.T) {
		r := launch(t, "linger")

		for {
			select {
			case code := <-r.code:
				if code != 0 {
					t.Errorf("run = %d, want 0: the app finished cleanly without ever listening", code)
				}
				return
			default:
			}
			conn, err := net.DialTimeout("tcp", "127.0.0.1:"+r.port, 50*time.Millisecond)
			if err == nil {
				conn.Close()
				t.Fatal("the exposed port answered while the app was not listening: Cloud Run sends requests as soon as the port is open and holds them until then, so a front that opens first turns a cold start's queued requests into refusals")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	t.Run("the app's exit code is the runtime's", func(t *testing.T) {
		r := launch(t, "exit:3")

		if code := r.await(t); code != 3 {
			t.Errorf("run = %d, want 3: the platform reads the app's own exit code off the runtime", code)
		}
	})

	t.Run("a signal to the runtime reaches the app", func(t *testing.T) {
		held := make(chan os.Signal, 1)
		signal.Notify(held, syscall.SIGTERM)
		t.Cleanup(func() { signal.Stop(held) })

		r := launch(t, "serve")
		r.awaitFront(t, "")

		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if code := r.await(t); code != 0 {
			t.Errorf("run = %d, want 0: the app was asked to stop and finished on its own terms", code)
		}
	})
}
