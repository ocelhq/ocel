package main

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	variables "github.com/ocelhq/ocel/platform/gcp/provider/resources/variables/live"
)

const (
	roleVar    = "OCEL_TEST_CONTAINER_ROLE"
	reportVar  = "OCEL_TEST_CONTAINER_REPORT"
	healthPath = "/_ocel/health"
	lingerFor  = 400 * time.Millisecond
)

type served struct {
	Port     string `json:"port"`
	Secret   string `json:"secret"`
	Health   string `json:"health"`
	Runtime  string `json:"runtime"`
	Token    string `json:"token"`
	Gate     string `json:"gate"`
	GateFile string `json:"gateFile"`
	Probe    string `json:"probe"`
	Host     string `json:"host"`
	Named    string `json:"named"`
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

	if role == "report" {
		if err := os.WriteFile(os.Getenv(reportVar), []byte(os.Getenv(containerimage.NodeOptionsEnvVar)), 0o600); err != nil {
			os.Exit(96)
		}
		os.Exit(0)
	}
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
	if role == "worker" {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`"resized"`))
			go func() {
				time.Sleep(50 * time.Millisecond)
				os.Exit(0)
			}()
		})
		host := cmp.Or(os.Getenv("HOST"), "127.0.0.1")
		if err := http.ListenAndServe(host+":"+os.Getenv(containerimage.PortEnvVar), mux); err != nil {
			os.Exit(97)
		}
		return
	}
	mux.HandleFunc("/quit", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		go func() {
			time.Sleep(50 * time.Millisecond)
			os.Exit(0)
		}()
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(served{
			Host:     r.Host,
			Named:    r.Header.Get("X-Forwarded-Host"),
			Port:     os.Getenv(containerimage.PortEnvVar),
			Secret:   os.Getenv(edge.OriginSecretVar),
			Health:   os.Getenv(originguard.HealthPathVar),
			Runtime:  os.Getenv(processenv.RuntimeAddressEnvVar),
			Token:    os.Getenv(localrpc.SessionTokenEnvVar),
			Gate:     os.Getenv(gate.MountedInlineVar),
			GateFile: os.Getenv(gate.MountedFileVar),
			Probe:    os.Getenv(originguard.ProbeTokenVar),
		})
	})
	if err := http.ListenAndServe("127.0.0.1:"+os.Getenv(containerimage.PortEnvVar), mux); err != nil {
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
	return launchHolding(t, fileExists, role, environ...)
}

func launchHolding(t *testing.T, present func(string) bool, role string, environ ...string) *running {
	t.Helper()
	port := exposedPort(t)
	command := appCommand(t)
	environ = append([]string{containerimage.PortEnvVar + "=" + port}, environ...)

	out := &running{port: port, code: make(chan int, 1)}
	go func() { out.code <- run(context.Background(), command, append(environ, roleVar+"="+role), present) }()
	return out
}

func (r *running) ask(t *testing.T, path, secret string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+r.port+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		req.Header.Set(edge.OriginSecretHeader, secret)
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
	return r.awaitFrontWith(t, secret, nil)
}

func (r *running) awaitFrontWith(t *testing.T, secret string, header http.Header) served {
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
		for name, values := range header {
			req.Header[name] = values
		}
		if secret != "" {
			req.Header.Set(edge.OriginSecretHeader, secret)
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
		req.Header.Set(edge.OriginSecretHeader, secret)
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
		if code := run(context.Background(), nil, nil, fileExists); code != 1 {
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
			t.Errorf("the app was handed %s=%s, the very port the platform routes to; the front must own the exposed port", containerimage.PortEnvVar, body.Port)
		}
		if _, err := strconv.Atoi(body.Port); err != nil {
			t.Errorf("the app was handed %s=%q, which is no port", containerimage.PortEnvVar, body.Port)
		}
		if code := r.quit(t, ""); code != 0 {
			t.Errorf("run = %d, want 0 for an app that finished cleanly", code)
		}
	})

	t.Run("the front gates every request against the gate its service mounts and never hands the app the gate", func(t *testing.T) {
		held := `{"key":"k","passwordHash":"` + gate.HashPassword("pw") + `"}`
		mounted := filepath.Join(t.TempDir(), "gate")
		if err := os.WriteFile(mounted, []byte(held), 0o600); err != nil {
			t.Fatal(err)
		}
		r := launch(t, "serve", gate.MountedFileVar+"="+mounted, originguard.HealthPathVar+"=/", originguard.ProbeTokenVar+"=probe-token")
		signedIn := http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("anyone:pw"))}}
		body := r.awaitFrontWith(t, "", signedIn)

		if body.Probe != "" {
			t.Errorf("the app was handed %s=%q; the token Cloud Run's probe carries is not the app's", originguard.ProbeTokenVar, body.Probe)
		}
		probe, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+r.port+"/", nil)
		probe.Header.Set(originguard.ProbeTokenHeader, "probe-token")
		if resp, err := http.DefaultClient.Do(probe); err != nil || resp.StatusCode != http.StatusOK {
			t.Errorf("a probe carrying the token = %v, %v, want the app's 200", resp, err)
		} else {
			resp.Body.Close()
		}

		if body.GateFile != "" || body.Gate != "" {
			t.Errorf("the app was handed %s=%q and %s=%q; where its gate lives is not the app's",
				gate.MountedFileVar, body.GateFile, gate.MountedInlineVar, body.Gate)
		}
		if resp := r.ask(t, "/", ""); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("a request with no credentials = %d, want 401: nothing else in front of this service gates its preview", resp.StatusCode)
		}
		req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+r.port+"/quit", nil)
		req.Header = signedIn
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
		if code := r.await(t); code != 0 {
			t.Errorf("run = %d, want 0", code)
		}
	})

	t.Run("a service whose mounted gate cannot be read serves no one", func(t *testing.T) {
		sigterm := make(chan os.Signal, 1)
		signal.Notify(sigterm, syscall.SIGTERM)
		t.Cleanup(func() { signal.Stop(sigterm) })
		missing := filepath.Join(t.TempDir(), "gate")
		r := launch(t, "serve", gate.MountedFileVar+"="+missing)
		deadline := time.Now().Add(30 * time.Second)
		for resp := (*http.Response)(nil); ; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the front never answered")
			}
			var err error
			if resp, err = http.Get("http://127.0.0.1:" + r.port + "/"); err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("a request with the gate unreadable = %d, want 503", resp.StatusCode)
			}
			break
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		r.await(t)
	})

	t.Run("the origin secret guards the front and never reaches the app", func(t *testing.T) {
		const secret = "s3cr3t"
		r := launch(t, "serve", edge.OriginSecretVar+"="+secret)
		body := r.awaitFront(t, secret)

		if body.Secret != "" {
			t.Errorf("the app was handed %s=%q; nothing the app runs or logs may contain the origin secret", edge.OriginSecretVar, body.Secret)
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

	t.Run("the app hears the visitor's host the worker forwarded, not the revision-tag URL's", func(t *testing.T) {
		const (
			secret  = "s3cr3t"
			visitor = "shop.example.com"
		)
		r := launch(t, "serve", edge.OriginFrontedVar+"=1", edge.OriginSecretVar+"="+secret)
		body := r.awaitFrontWith(t, secret, http.Header{"X-Forwarded-Host": {visitor}})

		if body.Host != visitor || body.Named != visitor {
			t.Errorf("the app heard Host %q and X-Forwarded-Host %q, want %q for both: an app that checks or redirects to its host would answer for the tag URL, which refuses the visitor", body.Host, body.Named, visitor)
		}
		if code := r.quit(t, secret); code != 0 {
			t.Errorf("run = %d, want 0", code)
		}
	})

	t.Run("a front marked fronted and handed no origin secret refuses to start", func(t *testing.T) {
		r := launch(t, "serve", edge.OriginFrontedVar+"=1")

		if code := r.await(t); code != 1 {
			t.Errorf("run = %d, want 1: a deployment reached only through its front would admit any caller who found it", code)
		}
	})

	t.Run("a front marked fronted guards with the origin secret it is handed", func(t *testing.T) {
		const secret = "s3cr3t"
		r := launch(t, "serve", edge.OriginFrontedVar+"=1", edge.OriginSecretVar+"="+secret)
		r.awaitFront(t, secret)

		if resp := r.ask(t, "/", ""); resp.StatusCode != http.StatusForbidden {
			t.Errorf("status without the secret = %d, want %d", resp.StatusCode, http.StatusForbidden)
		}
		if code := r.quit(t, secret); code != 0 {
			t.Errorf("run = %d, want 0", code)
		}
	})

	t.Run("Cloud Run's probe carrying its token is answered without the secret, and no other request on the health path is", func(t *testing.T) {
		const secret = "s3cr3t"
		r := launch(t, "serve", edge.OriginSecretVar+"="+secret, originguard.HealthPathVar+"="+healthPath, originguard.ProbeTokenVar+"=probe-token")
		body := r.awaitFront(t, secret)

		if body.Health != healthPath {
			t.Errorf("the app was handed %s=%q, want %q: the app serves the path the platform probes", originguard.HealthPathVar, body.Health, healthPath)
		}
		probe, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+r.port+healthPath, nil)
		probe.Header.Set(originguard.ProbeTokenHeader, "probe-token")
		if resp, err := http.DefaultClient.Do(probe); err != nil || resp.StatusCode != http.StatusOK {
			t.Errorf("a probe carrying the token = %v, %v, want the app's 200: the startup probe sends no secret and its failure takes the deployment down", resp, err)
		} else {
			resp.Body.Close()
		}
		if resp := r.ask(t, healthPath, ""); resp.StatusCode != http.StatusForbidden {
			t.Errorf("a request on the health path with neither the secret nor the token = %d, want %d", resp.StatusCode, http.StatusForbidden)
		}
		if code := r.quit(t, secret); code != 0 {
			t.Errorf("run = %d, want 0", code)
		}
	})

	t.Run("a front holding the origin secret and no probe token runs no app code for a request on the health path without the secret", func(t *testing.T) {
		const secret = "s3cr3t"
		r := launch(t, "serve", edge.OriginSecretVar+"="+secret, originguard.HealthPathVar+"="+healthPath)
		r.awaitFront(t, secret)

		if resp := r.ask(t, healthPath, ""); resp.StatusCode != http.StatusForbidden {
			t.Errorf("status on the health path without the secret = %d, want %d: Cloud Run's probe carries a token, so a caller without one came round the edge", resp.StatusCode, http.StatusForbidden)
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
				t.Fatal("the exposed port answered while the app was not listening: Cloud Run sends requests as soon as the port is open and queues them until then, so a front that opens first turns a cold start's queued requests into refusals")
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
		sigterm := make(chan os.Signal, 1)
		signal.Notify(sigterm, syscall.SIGTERM)
		t.Cleanup(func() { signal.Stop(sigterm) })

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

func imageWith(files ...string) func(string) bool {
	return func(path string) bool { return slices.Contains(files, path) }
}

func reportedNodeOptions(t *testing.T, present func(string) bool, environ ...string) string {
	t.Helper()
	report := filepath.Join(t.TempDir(), "node-options")
	r := launchHolding(t, present, "report", append(environ, reportVar+"="+report)...)
	if code := r.await(t); code != 0 {
		t.Fatalf("run = %d, want 0 from an app that reported its node options", code)
	}
	got, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("the app reported nothing: %v", err)
	}
	return string(got)
}

func TestTheRuntimeRunsANextContainersAppWithTheServerPreloadAfterTheNodeOptionsItIsGiven(t *testing.T) {
	preload := "--import " + containerimage.NextServerPreloadPath
	next := imageWith(containerimage.NextServerPreloadPath)

	if got := reportedNodeOptions(t, next); got != preload {
		t.Errorf("NODE_OPTIONS = %q with none given, want %q", got, preload)
	}
	if got, want := reportedNodeOptions(t, next, containerimage.NodeOptionsEnvVar+"=--max-old-space-size=512"), "--max-old-space-size=512 "+preload; got != want {
		t.Errorf("NODE_OPTIONS = %q, want %q: the image's or the deploy's node options are kept and the preload follows them", got, want)
	}
}

func TestTheRuntimeLeavesTheNodeOptionsOfAContainerWithoutTheNextServerPreload(t *testing.T) {
	if got := reportedNodeOptions(t, imageWith(), containerimage.NodeOptionsEnvVar+"=--max-old-space-size=512"); got != "--max-old-space-size=512" {
		t.Errorf("NODE_OPTIONS = %q, want the image's own %q", got, "--max-old-space-size=512")
	}
}

func TestTheRuntimeRunsAWorkerInANextContainerWithoutTheServerPreload(t *testing.T) {
	manifest, err := variables.Render(variables.Manifest{
		Project: "p", Region: "europe-west1", Namespace: "ocel", Slug: "shop", Tier: "preview",
		Tasks: &variables.Tasks{Environment: "t", Topics: map[string]provider.TopicSpec{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := reportedNodeOptions(t, imageWith(containerimage.NextServerPreloadPath),
		containerimage.NodeOptionsEnvVar+"=--max-old-space-size=512", processenv.WorkerEnvVar+"=worker", variables.EnvVar+"="+string(manifest))

	if got != "--max-old-space-size=512" {
		t.Errorf("NODE_OPTIONS = %q, want the image's own %q: a worker is not the Next server the preload is for", got, "--max-old-space-size=512")
	}
}
