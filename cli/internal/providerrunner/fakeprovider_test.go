package providerrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
)

const fakeProviderModeEnvVar = "OCEL_TEST_FAKE_PROVIDER_MODE"

const fakeProviderSockEnvVar = "OCEL_TEST_FAKE_PROVIDER_SOCK"

const fakeProviderGrandchildPidFileEnvVar = "OCEL_TEST_FAKE_PROVIDER_GRANDCHILD_PIDFILE"

func runFakeProvider() int {
	mode := os.Getenv(fakeProviderModeEnvVar)

	switch mode {
	case "exit-before-ready":
		fmt.Fprintln(os.Stderr, "fake provider: simulated startup failure")
		return 7
	case "never-ready":
		select {}
	case "oversized-line":
		os.Stdout.Write(bytes.Repeat([]byte("x"), 2*1024*1024))
		fmt.Println()
		select {}
	case "orphan-holds-pipe", "orphan-detached-pipe":
		if err := spawnGrandchildSurvivor(mode == "orphan-holds-pipe"); err != nil {
			fmt.Fprintln(os.Stderr, "fake provider: spawn grandchild:", err)
			return 1
		}
		select {}
	case "grandchild-survivor":
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		time.Sleep(10 * time.Second)
		return 0
	}

	sockPath := os.Getenv(fakeProviderSockEnvVar)
	if sockPath == "" {
		fmt.Fprintln(os.Stderr, "fake provider: missing socket path")
		return 1
	}
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: listen:", err)
		return 1
	}
	defer ln.Close()

	mux := http.NewServeMux()
	path, handler := deploymentsv1connect.NewDeploymentServiceHandler(&fakeProviderServer{
		mode:  mode,
		token: os.Getenv(channel.SessionTokenEnvVar),
	})
	mux.Handle(path, handler)

	fmt.Println(channel.FormatReadinessLine(channel.FormatUnixAddr(sockPath)))

	srv := &http.Server{Handler: mux}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return 1
	}
	return 0
}

type fakeProviderServer struct {
	deploymentsv1connect.UnimplementedDeploymentServiceHandler
	mode  string
	token string
}

func (s *fakeProviderServer) Deploy(ctx context.Context, req *deploymentsv1.DeployRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	info, _ := connect.CallInfoForHandlerContext(ctx)
	var authHeader string
	if info != nil {
		authHeader = info.RequestHeader().Get("Authorization")
	}
	if token, ok := channel.ParseAuthHeader(authHeader); !ok || token != s.token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("bad or missing session token"))
	}

	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "step 1"}},
	}); err != nil {
		return err
	}

	switch s.mode {
	case "fail":
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: false, Error: "simulated deploy failure"}},
		})
	case "hang-deploy":
		time.Sleep(30 * time.Second)
		return nil
	default:
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
		})
	}
}

func (s *fakeProviderServer) Bootstrap(ctx context.Context, req *deploymentsv1.BootstrapRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	info, _ := connect.CallInfoForHandlerContext(ctx)
	var authHeader string
	if info != nil {
		authHeader = info.RequestHeader().Get("Authorization")
	}
	if token, ok := channel.ParseAuthHeader(authHeader); !ok || token != s.token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("bad or missing session token"))
	}

	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "bootstrapping"}},
	}); err != nil {
		return err
	}
	if s.mode == "fail" {
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: false, Error: "simulated bootstrap failure"}},
		})
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}

func spawnGrandchildSurvivor(holdPipe bool) error {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), fakeProviderEnvVar+"=1", fakeProviderModeEnvVar+"=grandchild-survivor")
	if holdPipe {
		cmd.Stdout = os.Stdout
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	pidFile := os.Getenv(fakeProviderGrandchildPidFileEnvVar)
	if pidFile == "" {
		return errors.New("missing grandchild pidfile path")
	}
	return os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0o600)
}
