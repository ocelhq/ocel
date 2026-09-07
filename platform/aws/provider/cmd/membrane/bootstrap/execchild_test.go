package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	helperChildVar  = "OCEL_TEST_HTTP_CHILD"
	helperParentVar = "OCEL_TEST_HTTP_CHILD_PARENT"
	helperSaidVar   = "OCEL_TEST_HTTP_CHILD_SAYS"
)

func TestHTTPChildHelper(t *testing.T) {
	if os.Getenv(helperChildVar) == "" {
		t.Skip("this test is the process the exec child tests host")
	}
	parent, _ := strconv.Atoi(os.Getenv(helperParentVar))
	go func() {
		for os.Getppid() == parent {
			time.Sleep(100 * time.Millisecond)
		}
		os.Exit(0)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "served by %s with %s", filepath.Base(os.Args[0]), os.Getenv(helperSaidVar))
	})
	if err := http.ListenAndServe("127.0.0.1:"+os.Getenv("PORT"), mux); err != nil {
		os.Exit(1)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func httpChildCommand(t *testing.T) []string {
	t.Helper()
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, filepath.Join(root, "server")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAMBDA_TASK_ROOT", root)
	return []string{"./server", "-test.run=TestHTTPChildHelper"}
}

func helperEnv() []string {
	return []string{
		helperChildVar + "=1",
		helperParentVar + "=" + strconv.Itoa(os.Getpid()),
	}
}

func TestStartExecutable(t *testing.T) {
	t.Run("execs the artifact's own command and waits for the port it was told to bind", func(t *testing.T) {
		command := httpChildCommand(t)
		port := freePort(t)

		child, err := startExecutable(command, port, append(helperEnv(), helperSaidVar+"=what it was handed"), 20*time.Second)
		if err != nil {
			t.Fatalf("startExecutable: %v", err)
		}
		if child.endpoint().port != port {
			t.Errorf("port = %d, want %d", child.endpoint().port, port)
		}

		rt, captured := fakeRuntime(t, []byte(getEvent))
		if err := handleInvocation(t.Context(), rt, child); err != nil {
			t.Fatalf("handleInvocation: %v", err)
		}
		_, body := splitPrelude(t, captured.body)
		if want := "served by server with what it was handed"; string(body) != want {
			t.Errorf("body = %q, want %q — the child is exec'd from the artifact with the env the membrane resolved", body, want)
		}
	})

	t.Run("answers a warm invocation without a compile cache to warm", func(t *testing.T) {
		command := httpChildCommand(t)
		child, err := startExecutable(command, freePort(t), helperEnv(), 20*time.Second)
		if err != nil {
			t.Fatalf("startExecutable: %v", err)
		}

		rt, captured := fakeRuntime(t, []byte(warmEvent))
		if err := handleInvocation(t.Context(), rt, child); err != nil {
			t.Fatalf("handleInvocation: %v", err)
		}
		var summary warmSummary
		if err := json.Unmarshal(captured.body, &summary); err != nil {
			t.Fatalf("answer is not a warm summary (%q): %v", captured.body, err)
		}
		if summary.State != warmStateDisabled {
			t.Errorf("state = %q, want %q", summary.State, warmStateDisabled)
		}
		if summary.Source != bytecodeSourceNone {
			t.Errorf("source = %q, want %q", summary.Source, bytecodeSourceNone)
		}
	})

	t.Run("carries none of the hooks the membrane holds a control channel for", func(t *testing.T) {
		var c child = &execChild{}
		if _, controlled := c.(controlledChild); controlled {
			t.Error("an exec'd child answers hooks that exist only for a child the membrane drives over a control socket")
		}
	})

	t.Run("reports the exit of a command that dies before it listens", func(t *testing.T) {
		_, err := startExecutable([]string{"sh", "-c", "exit 3"}, freePort(t), nil, 20*time.Second)
		if err == nil {
			t.Fatal("startExecutable() error = nil, want the child's exit reported")
		}
		if !strings.Contains(err.Error(), "exit status 3") {
			t.Errorf("error = %q, want it to carry the exit status the app died with", err)
		}
	})

	t.Run("refuses a command that never listens within the budget", func(t *testing.T) {
		_, err := startExecutable([]string{"sh", "-c", "sleep 1"}, freePort(t), nil, 150*time.Millisecond)
		if err == nil {
			t.Fatal("startExecutable() error = nil, want a budget-expiry error")
		}
		if !strings.Contains(err.Error(), "150ms") {
			t.Errorf("error = %q, want it to name the budget that expired", err)
		}
	})

	t.Run("refuses a command that is not there", func(t *testing.T) {
		t.Setenv("LAMBDA_TASK_ROOT", t.TempDir())
		_, err := startExecutable([]string{"./nothing-was-built"}, freePort(t), nil, time.Second)
		if err == nil || !strings.Contains(err.Error(), "nothing-was-built") {
			t.Fatalf("error = %v, want a refusal naming the command the artifact declared", err)
		}
	})
}

func TestExecutableEnvNamesThePortTheAppBinds(t *testing.T) {
	env := executableEnv(4321, []string{"OCEL_RUNTIME_ADDRESS=http://127.0.0.1:9"})
	var port, address string
	for _, entry := range env {
		if name, value, _ := strings.Cut(entry, "="); name == "PORT" {
			port = value
		} else if name == "OCEL_RUNTIME_ADDRESS" {
			address = value
		}
	}
	if port != "4321" {
		t.Errorf("PORT = %q, want the port the membrane forwards to", port)
	}
	if address != "http://127.0.0.1:9" {
		t.Errorf("OCEL_RUNTIME_ADDRESS = %q, want what the membrane resolved for this deployment", address)
	}
}

func TestReadArtifactCarriesTheCommandItIsServedBy(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMBDA_TASK_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "config.json"),
		[]byte(`{"runtime":{"name":"go"},"handler":"web","command":["./web"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	served := readArtifact()
	if !served.executable() {
		t.Fatalf("artifact %+v is not read as one the membrane execs", served)
	}
	if len(served.Command) != 1 || served.Command[0] != "./web" {
		t.Errorf("command = %q, want the artifact's own binary", served.Command)
	}
	if readArtifact().Runtime.Name != "go" {
		t.Errorf("runtime = %q, want what the artifact declares", served.Runtime.Name)
	}
}

func TestAnArtifactWithoutACommandIsHostedByNode(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMBDA_TASK_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "config.json"),
		[]byte(`{"runtime":{"name":"node"},"handler":"index.mjs"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if readArtifact().executable() {
		t.Error("a bundled artifact declares no command, and the membrane hosts it in node")
	}
}
