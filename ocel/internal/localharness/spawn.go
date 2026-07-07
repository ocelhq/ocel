// Package localharness spawns a local HTTP process that implements the same
// provisioning handshake the real Ocel API will, and speaks its protocol as
// an HTTP client -- letting devserver.WithProvisioner route provisioning
// through a fast-starting local process instead of deadlocking on a
// control-plane API that can't be up yet during self-hosted dev.
package localharness

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"time"
)

// Process is a local HTTP process spawned by Spawn, reachable at Addr once
// it has been confirmed healthy.
type Process struct {
	cmd    *exec.Cmd
	Addr   string
	exited chan error
}

// SpawnConfig configures Spawn.
type SpawnConfig struct {
	Command string
	Args    []string
	Env     []string
	Dir     string

	// HealthPath is polled with GET until it answers 200 OK. Defaults to
	// "/health".
	HealthPath string
	// StartTimeout bounds how long Spawn waits for HealthPath to answer 200
	// OK before killing the process and failing. Defaults to 10s.
	StartTimeout time.Duration
}

// Spawn starts cfg.Command on a free localhost port, injected into the
// child's environment as PORT, then polls cfg.HealthPath until it answers
// 200 OK. It fails fast if the process exits before becoming healthy, and
// kills the process if cfg.StartTimeout elapses first.
func Spawn(ctx context.Context, cfg SpawnConfig) (*Process, error) {
	if cfg.HealthPath == "" {
		cfg.HealthPath = "/health"
	}
	if cfg.StartTimeout == 0 {
		cfg.StartTimeout = 10 * time.Second
	}

	port, closePort, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("find free port: %w", err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	cmd.Env = append(append([]string{}, cfg.Env...), fmt.Sprintf("PORT=%d", port))

	// Hold the reservation open until immediately before Start, to shrink
	// the window in which another process could grab the port first.
	closePort()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	healthCtx, cancel := context.WithTimeout(ctx, cfg.StartTimeout)
	defer cancel()

	healthURL := "http://" + addr + cfg.HealthPath
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-exited:
			return nil, fmt.Errorf("process exited before becoming healthy: %w", err)
		case <-healthCtx.Done():
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("process did not become healthy within %s", cfg.StartTimeout)
		case <-ticker.C:
			req, err := http.NewRequestWithContext(healthCtx, http.MethodGet, healthURL, nil)
			if err != nil {
				continue
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return &Process{cmd: cmd, Addr: addr, exited: exited}, nil
			}
		}
	}
}

// Stop kills the process and waits for it to exit.
func (p *Process) Stop() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	<-p.exited
}

// freePort returns a free port and a close function that releases the
// reservation. The caller should hold the reservation open for as long as
// possible and close it only immediately before the real listener binds, to
// minimize the window in which another process could steal the port.
func freePort() (int, func(), error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, err
	}
	return l.Addr().(*net.TCPAddr).Port, func() { l.Close() }, nil
}
