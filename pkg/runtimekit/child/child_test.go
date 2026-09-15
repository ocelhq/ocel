package child

import (
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	roleVar  = "OCEL_TEST_CHILD_ROLE"
	readyVar = "OCEL_TEST_CHILD_READY"
)

func TestChildHelper(t *testing.T) {
	role := os.Getenv(roleVar)
	if role == "" {
		t.Skip("this test is the process the child tests drive")
	}
	go func() {
		time.Sleep(60 * time.Second)
		os.Exit(99)
	}()

	if strings.HasPrefix(role, "exit:") {
		code, err := strconv.Atoi(strings.TrimPrefix(role, "exit:"))
		if err != nil {
			os.Exit(98)
		}
		os.Exit(code)
	}

	var ln net.Listener
	term := make(chan os.Signal, 1)
	switch role {
	case "listen":
		opened, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("PORT"))
		if err != nil {
			os.Exit(97)
		}
		ln = opened
	case "deaf":
		signal.Ignore(syscall.SIGTERM, syscall.SIGINT)
	case "obeys":
		signal.Notify(term, syscall.SIGTERM)
	default:
		os.Exit(96)
	}

	if err := os.WriteFile(os.Getenv(readyVar), []byte("1"), 0o600); err != nil {
		os.Exit(95)
	}

	switch role {
	case "obeys":
		<-term
		os.Exit(0)
	case "deaf":
		select {}
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			os.Exit(0)
		}
		conn.Close()
	}
}

func start(t *testing.T, role string, env ...string) *Process {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	proc, err := Start(Options{
		Command: []string{binary, "-test.run=^TestChildHelper$"},
		Env:     append([]string{roleVar + "=" + role, readyVar + "=" + ready}, env...),
		Stdout:  io.Discard,
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if strings.HasPrefix(role, "exit:") {
		return proc
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			return proc
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the helper child never came up")
	return nil
}

func waitExit(t *testing.T, proc *Process) Exit {
	t.Helper()
	select {
	case exit := <-proc.Exited():
		return exit
	case <-time.After(20 * time.Second):
		t.Fatal("the child never reported its exit")
		return Exit{}
	}
}

func TestStart(t *testing.T) {
	t.Run("refuses to start nothing", func(t *testing.T) {
		if _, err := Start(Options{}); err == nil {
			t.Fatal("Start = nil, want a runtime with no command to run refused")
		}
	})

	t.Run("names the command it could not start", func(t *testing.T) {
		_, err := Start(Options{Command: []string{"./nothing-was-built", "--serve"}})
		if err == nil {
			t.Fatal("Start = nil, want a command that is not there refused")
		}
		if !strings.Contains(err.Error(), "nothing-was-built") {
			t.Errorf("error = %v, want it to name the command", err)
		}
	})
}

func TestExited(t *testing.T) {
	t.Run("carries the code the app died with", func(t *testing.T) {
		exit := waitExit(t, start(t, "exit:3"))

		if exit.Code != 3 {
			t.Errorf("code = %d, want 3: the runtime propagates what the app chose", exit.Code)
		}
		if exit.Err == nil || !strings.Contains(exit.Error(), "exit status 3") {
			t.Errorf("error = %v, want the exit status named", exit.Err)
		}
	})

	t.Run("says nothing at all for an app that finished cleanly", func(t *testing.T) {
		exit := waitExit(t, start(t, "exit:0"))

		if exit.Code != 0 || exit.Err != nil {
			t.Errorf("exit = %+v, want a clean exit reported as one", exit)
		}
		if exit.Error() != "exit status 0" {
			t.Errorf("Error() = %q, want the status spelled out", exit.Error())
		}
	})

	t.Run("reports an app killed by a signal as the shell reports it", func(t *testing.T) {
		proc := start(t, "deaf")
		if err := proc.Signal(syscall.SIGKILL); err != nil {
			t.Fatalf("signal: %v", err)
		}

		exit := waitExit(t, proc)
		if exit.Code != 128+int(syscall.SIGKILL) {
			t.Errorf("code = %d, want %d", exit.Code, 128+int(syscall.SIGKILL))
		}
		if exit.Err == nil || !strings.Contains(exit.Error(), "killed") {
			t.Errorf("error = %v, want the kill named rather than an exit status", exit.Err)
		}
	})

	t.Run("names the process it started", func(t *testing.T) {
		proc := start(t, "exit:0")
		if proc.PID() <= 0 {
			t.Errorf("PID() = %d, want the pid signals are addressed to", proc.PID())
		}
		waitExit(t, proc)
	})
}

func TestStop(t *testing.T) {
	t.Run("asks the app to finish and lets it exit on its own terms", func(t *testing.T) {
		proc := start(t, "obeys")

		exit := proc.Stop(20 * time.Second)
		if exit.Code != 0 || exit.Err != nil {
			t.Errorf("exit = %+v, want the app's own clean exit after SIGTERM", exit)
		}
	})

	t.Run("kills an app that will not go after the grace", func(t *testing.T) {
		proc := start(t, "deaf")

		began := time.Now()
		exit := proc.Stop(200 * time.Millisecond)
		took := time.Since(began)

		if exit.Code != 128+int(syscall.SIGKILL) {
			t.Errorf("code = %d, want %d: an app that ignores SIGTERM is killed", exit.Code, 128+int(syscall.SIGKILL))
		}
		if took < 200*time.Millisecond {
			t.Errorf("the kill landed after %s, want the app given its whole grace first", took)
		}
	})
}

func TestWatchListening(t *testing.T) {
	t.Run("resolves once the app has the port open", func(t *testing.T) {
		port, err := FreePort()
		if err != nil {
			t.Fatal(err)
		}
		proc := start(t, "listen", "PORT="+strconv.Itoa(port))
		t.Cleanup(func() { proc.Stop(5 * time.Second) })

		select {
		case err := <-WatchListening("127.0.0.1:"+strconv.Itoa(port), proc.Exited()):
			if err != nil {
				t.Fatalf("WatchListening = %v, want the app served once it listened", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("WatchListening never resolved for an app that came up")
		}
	})

	t.Run("reports an app that exited before it ever listened", func(t *testing.T) {
		port, err := FreePort()
		if err != nil {
			t.Fatal(err)
		}
		proc := start(t, "exit:3")

		select {
		case err := <-WatchListening("127.0.0.1:"+strconv.Itoa(port), proc.Exited()):
			if err == nil {
				t.Fatal("WatchListening = nil, want the app's exit reported rather than a wait that never ends")
			}
			for _, want := range []string{strconv.Itoa(port), "exit status 3"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %v, want it to carry %q", err, want)
				}
			}
		case <-time.After(20 * time.Second):
			t.Fatal("WatchListening waited on an app that was never going to listen")
		}
	})
}

func TestFreePort(t *testing.T) {
	t.Run("names a port the app can bind", func(t *testing.T) {
		port, err := FreePort()
		if err != nil {
			t.Fatalf("FreePort: %v", err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			t.Fatalf("listen on the port handed out: %v", err)
		}
		ln.Close()
	})
}
