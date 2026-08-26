package provider

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
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/procgroup"
	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/naming"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const fakeProviderModeEnvVar = "OCEL_TEST_FAKE_PROVIDER_MODE"

const fakeProviderSockEnvVar = "OCEL_TEST_FAKE_PROVIDER_SOCK"

const fakeProviderGrandchildPidFileEnvVar = "OCEL_TEST_FAKE_PROVIDER_GRANDCHILD_PIDFILE"

const fakeProviderKnownHostsEnvVar = "OCEL_TEST_FAKE_PROVIDER_KNOWN_HOSTS"

const fakeProviderDrivesEnvVar = "OCEL_TEST_FAKE_PROVIDER_DRIVES"

const (
	fakeHostName     = "vps.example.com"
	fakeHostAddress  = "203.0.113.7"
	fakeHostPort     = 2222
	fakeHostKeyType  = "ssh-ed25519"
	fakeHostKey      = "AAAAC3NzaC1lZDI1NTE5AAAAIGjxLv2WrJFcWFzVC/ui/P691jGR92crO0DsjeqiPi54"
	fakeOtherHostKey = "AAAAC3NzaC1lZDI1NTE5AAAAIBtlSdtoFVwXcyx7e4GZ4N/zr7JGNG3D6kjc2ceBg1Ag"
)

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
	case "orphan-holds-pipe":
		if err := spawnGrandchildSurvivor(true, true); err != nil {
			fmt.Fprintln(os.Stderr, "fake provider: spawn grandchild:", err)
			return 1
		}
		select {}
	case "orphan-detached-pipe":
		if err := spawnGrandchildSurvivor(false, false); err != nil {
			fmt.Fprintln(os.Stderr, "fake provider: spawn grandchild:", err)
			return 1
		}
		select {}
	case "grandchild-survivor":
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			fmt.Println("grandchild still holds the pipe")
			time.Sleep(20 * time.Millisecond)
		}
		return 0
	}

	sockPath := os.Getenv(fakeProviderSockEnvVar)
	if sockPath == "" {
		fmt.Fprintln(os.Stderr, "fake provider: missing socket path")
		return 1
	}
	_ = os.Remove(sockPath)

	bound, err := net.Listen("unix", sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: listen:", err)
		return 1
	}
	defer bound.Close()

	mux := http.NewServeMux()
	path, handler := contractv1connect.NewProviderServiceHandler(&fakeProviderServer{mode: mode})
	mux.Handle(path, handler)

	if mode == "plaintext" {
		decoy, err := channel.NewIdentity()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake provider: decoy identity:", err)
			return 1
		}
		fmt.Println(channel.FormatReadinessLine(channel.FormatUnixAddr(sockPath), decoy.CertificateDER()))
		srv := &http.Server{Handler: mux}
		if err := srv.Serve(bound); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return 1
		}
		return 0
	}

	ln, identity, err := channel.SecureListener(bound)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider:", err)
		return 1
	}
	defer ln.Close()

	announced := identity
	if mode == "impostor-cert" {
		announced, err = channel.NewIdentity()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake provider: impostor identity:", err)
			return 1
		}
	}

	fmt.Println(channel.FormatReadinessLine(channel.FormatUnixAddr(sockPath), announced.CertificateDER()))

	srv := &http.Server{Handler: mux}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return 1
	}
	return 0
}

type fakeProviderServer struct {
	contractv1connect.UnimplementedProviderServiceHandler
	mode string
}

func (s *fakeProviderServer) Configure(_ context.Context, _ *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	if s.mode == "reject-config" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(`json: unknown field "regionn"`))
	}
	return &contractv1.ConfigureResponse{}, nil
}

var fakeStageID = naming.PhaseID(naming.UnitEnvironment, naming.PhaseProvisioning)

func (s *fakeProviderServer) Deploy(ctx context.Context, req *contractv1.DeployRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := stream.Send(&progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Progress{Progress: &progressv1.ProgressEvent{StageId: fakeStageID, Message: "step 1"}},
	}); err != nil {
		return err
	}

	switch s.mode {
	case "fail":
		return stream.Send(&progressv1.OperationEvent{
			Event: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{Success: false, Error: "simulated deploy failure"}},
		})
	case "hang-deploy":
		time.Sleep(30 * time.Second)
		return nil
	default:
		return stream.Send(&progressv1.OperationEvent{
			Event: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{Success: true}},
		})
	}
}

func (s *fakeProviderServer) Bootstrap(ctx context.Context, req *contractv1.BootstrapRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := recordDrive(); err != nil {
		return err
	}
	if err := refusalFor(s.mode); err != nil {
		return err
	}

	if err := stream.Send(&progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Progress{Progress: &progressv1.ProgressEvent{StageId: fakeStageID, Message: "bootstrapping"}},
	}); err != nil {
		return err
	}
	if s.mode == "fail" {
		return stream.Send(&progressv1.OperationEvent{
			Event: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{Success: false, Error: "simulated bootstrap failure"}},
		})
	}
	return stream.Send(&progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{Success: true}},
	})
}

func recordDrive() error {
	path := os.Getenv(fakeProviderDrivesEnvVar)
	if path == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.WriteString("drive\n"); err != nil {
		return err
	}
	return file.Sync()
}

func refusalFor(mode string) error {
	if mode != "unknown-host-key" && mode != "host-key-mismatch" {
		return nil
	}

	store := os.Getenv(fakeProviderKnownHostsEnvVar)
	trust := providerkit.HostTrust{
		Host:       fakeHostName,
		Address:    fakeHostAddress,
		Port:       fakeHostPort,
		KnownHosts: []string{store},
		Got:        fakeKey(fakeHostKey),
	}
	entry := trust.KnownHostsEntry()

	if mode == "host-key-mismatch" {
		trust.Reason = providerkit.HostKeyMismatch
		trust.Want = fakeKey(fakeOtherHostKey)
		trust.Remedy = fmt.Sprintf("ssh-keygen -R '%s' -f %s", entry, store)
		return providerkit.RefusalError(providerkit.RefuseHostTrust(trust))
	}

	if alreadyRecorded(store, entry, trust.Got) {
		return nil
	}
	trust.Reason = providerkit.UnknownHostKey
	trust.Remedy = fmt.Sprintf("ssh-keyscan -t %s -p %d %s >> %s", trust.Got.Type, fakeHostPort, fakeHostAddress, store)
	return providerkit.RefusalError(providerkit.RefuseHostTrust(trust))
}

func fakeKey(encoded string) providerkit.HostKey {
	key, err := (providerkit.HostKey{Type: fakeHostKeyType, Key: encoded}).Fingerprinted()
	if err != nil {
		panic(err)
	}
	return key
}

func alreadyRecorded(store, entry string, key providerkit.HostKey) bool {
	content, err := os.ReadFile(store)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == entry && fields[1] == key.Type && fields[2] == key.Key {
			return true
		}
	}
	return false
}

func spawnGrandchildSurvivor(holdPipe, ownGroup bool) error {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), fakeProviderEnvVar+"=1", fakeProviderModeEnvVar+"=grandchild-survivor")
	if holdPipe {
		cmd.Stdout = os.Stdout
	}
	if ownGroup {
		procgroup.Isolate(cmd)
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
