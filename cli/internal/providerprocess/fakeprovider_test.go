package providerprocess

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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/naming"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const fakeProviderModeEnvVar = "OCEL_TEST_PROVIDER_PROCESS_MODE"

const fakeProviderSockEnvVar = "OCEL_TEST_PROVIDER_PROCESS_SOCK"

const fakeProviderVendorEnvVar = "OCEL_TEST_PROVIDER_PROCESS_VENDOR"

const fakeProviderGrandchildPidFileEnvVar = "OCEL_TEST_PROVIDER_PROCESS_GRANDCHILD_PIDFILE"

const fakeProviderKnownHostsEnvVar = "OCEL_TEST_PROVIDER_PROCESS_KNOWN_HOSTS"

const fakeProviderDrivesEnvVar = "OCEL_TEST_PROVIDER_PROCESS_DRIVES"

const fakeProviderVersionEnvVar = "OCEL_TEST_PROVIDER_PROCESS_VERSION"

const fakeChattyLine = "fake provider: warming the cache"

func fakeProviderVersion() string {
	if announced := os.Getenv(fakeProviderVersionEnvVar); announced != "" {
		return announced
	}
	return version.Version
}

const (
	fakeHostName     = "box.example.com"
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
	case "orphan-keeps-pipe-open":
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
			fmt.Println("grandchild still has the pipe open")
			time.Sleep(20 * time.Millisecond)
		}
		return 0
	}

	if mode == "chatty" {
		fmt.Fprintln(os.Stderr, fakeChattyLine)
	}

	sockPath := os.Getenv(fakeProviderSockEnvVar)
	if sockPath == "" {
		fmt.Fprintln(os.Stderr, "fake provider: missing socket path")
		return 1
	}
	_ = os.Remove(sockPath)
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: socket directory:", err)
		return 1
	}

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
		decoy, err := localrpc.NewIdentity()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake provider: decoy identity:", err)
			return 1
		}
		fmt.Println(localrpc.FormatReadinessLine(fakeProviderVersion(), localrpc.FormatUnixAddress(sockPath), decoy.CertificateDER()))
		srv := &http.Server{Handler: mux}
		if err := srv.Serve(bound); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return 1
		}
		return 0
	}

	ln, identity, err := localrpc.SecureListener(bound)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider:", err)
		return 1
	}
	defer ln.Close()

	announced := identity
	if mode == "impostor-cert" {
		announced, err = localrpc.NewIdentity()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake provider: impostor identity:", err)
			return 1
		}
	}

	fmt.Println(localrpc.FormatReadinessLine(fakeProviderVersion(), localrpc.FormatUnixAddress(sockPath), announced.CertificateDER()))

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

type fakeOptions struct {
	Size    string `json:"size,omitempty"`
	Network struct {
		Host string `json:"host,omitempty"`
	} `json:"network,omitempty"`
}

func (s *fakeProviderServer) Configure(_ context.Context, req *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	switch s.mode {
	case "reject-config":
		if _, err := provider.Decode[fakeOptions](provider.Vendor(os.Getenv(fakeProviderVendorEnvVar)), provider.Options(req.GetConfig().GetOptions().AsMap())); err != nil {
			return nil, provider.RefusalError(err)
		}
	case "refuse-config":
		return nil, provider.RefusalError(refusal.Refuse(refusal.CodeInvalid, "this account is not bootstrapped for previews"))
	}
	return &contractv1.ConfigureResponse{}, nil
}

var fakeStageID = naming.UnitID(naming.UnitEnvironment)

const fakeOversizedEventBytes = 1 << 16

var fakePlan = &planv1.ChangePlan{Subject: "acme", Groups: []*planv1.ChangeGroup{{Kind: "stack", Name: "acme", Action: planv1.Change_ACTION_CREATE}}}

func fakeSaid(message string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{Level: progressv1.Level_LEVEL_INFO, SpanId: fakeStageID, Message: message}
}

func (s *fakeProviderServer) Deploy(ctx context.Context, req *contractv1.DeployRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if s.mode == "oversized-event" {
		return stream.Send(fakeSaid(strings.Repeat("x", fakeOversizedEventBytes)))
	}

	if err := stream.Send(fakeSaid("step 1")); err != nil {
		return err
	}

	if req.GetDry() {
		if err := stream.Send(&progressv1.OperationEvent{Body: &progressv1.OperationEvent_Plan{Plan: fakePlan}}); err != nil {
			return err
		}
	}

	switch s.mode {
	case "fail":
		return stream.Send(&progressv1.OperationEvent{
			Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: false, Error: "simulated deploy failure"}},
		})
	case "refuse-deploy":
		if err := stream.Send(&progressv1.OperationEvent{
			Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{
				Refused: true,
				Error:   "simulated deploy refusal",
				Apps: []*progressv1.AppResult{
					{App: "web", Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED},
					{App: "admin", Outcome: progressv1.AppOutcome_APP_OUTCOME_FAILED, Error: "simulated deploy refusal"},
				},
			}},
		}); err != nil {
			return err
		}
		return connect.NewError(connect.CodeInvalidArgument, errors.New("simulated deploy refusal"))
	case "hang-deploy":
		time.Sleep(30 * time.Second)
		return nil
	case "crash-deploy":
		fmt.Fprintln(os.Stderr, "panic: assignment to entry in nil map")
		os.Exit(2)
		return nil
	default:
		return stream.Send(&progressv1.OperationEvent{
			Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}},
		})
	}
}

func (s *fakeProviderServer) Preflight(context.Context, *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	if err := recordDrive(); err != nil {
		return nil, err
	}
	if err := refusalFor(s.mode); err != nil {
		return nil, askedOver(err)
	}
	return &contractv1.PreflightResponse{}, nil
}

func (s *fakeProviderServer) Bootstrap(ctx context.Context, req *contractv1.BootstrapRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := recordDrive(); err != nil {
		return err
	}
	if err := refusalFor(s.mode); err != nil {
		return askedOver(err)
	}

	if err := stream.Send(fakeSaid("bootstrapping")); err != nil {
		return err
	}
	if s.mode == "fail" {
		return stream.Send(&progressv1.OperationEvent{
			Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: false, Error: "simulated bootstrap failure"}},
		})
	}
	return stream.Send(&progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}},
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
	if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
		return err
	}
	return file.Sync()
}

const (
	fakeQuestionID      = "fake-question"
	fakeHostFingerprint = "SHA256:fake-host-key"
)

func fakeHostEntry() string { return fmt.Sprintf("[%s]:%d", fakeHostAddress, fakeHostPort) }

func fakeHostLine() string {
	return fmt.Sprintf("%s %s %s\n", fakeHostEntry(), fakeHostKeyType, fakeHostKey)
}

func refusalFor(mode string) error {
	if mode != "unknown-host-key" && mode != "host-key-mismatch" {
		return nil
	}

	store := os.Getenv(fakeProviderKnownHostsEnvVar)
	if mode == "host-key-mismatch" {
		return provider.RefusalError(refusal.Refuse(refusal.CodeDenied,
			"the host key for %s changed\nIf it was rebuilt, drop the old key and try again:\n  ssh-keygen -R '%s' -f %s",
			fakeHostName, fakeHostEntry(), store))
	}

	if alreadyRecorded(store) {
		return nil
	}
	finding := fmt.Sprintf("the host key for %s (%s) port %d is in none of %s\n  %s %s", fakeHostName, fakeHostAddress, fakeHostPort, store, fakeHostKeyType, fakeHostFingerprint)
	return provider.RefusalError(provider.Ask(
		fmt.Sprintf("%s\nCheck that fingerprint against the machine itself, then record it:\n  ssh-keyscan -t %s -p %d %s >> %s", finding, fakeHostKeyType, fakeHostPort, fakeHostAddress, store),
		provider.Question{Finding: finding, Prompt: fmt.Sprintf("Trust that key and record %s in %s?", fakeHostEntry(), store)},
	))
}

func askedOver(err error) error {
	question, asked := provider.QuestionOf(err)
	var wire *connect.Error
	if !asked || !errors.As(err, &wire) {
		return err
	}
	detail, detailErr := connect.NewErrorDetail(&contractv1.Question{Id: fakeQuestionID, Finding: question.Finding, Prompt: question.Prompt})
	if detailErr != nil {
		return errors.Join(err, detailErr)
	}
	wire.AddDetail(detail)
	return err
}

func (s *fakeProviderServer) Confirm(_ context.Context, req *contractv1.ConfirmRequest) (*contractv1.ConfirmResponse, error) {
	if req.GetQuestionId() != fakeQuestionID {
		return nil, provider.RefusalError(refusal.Refuse(refusal.CodeInvalid, "this provider is waiting on no question %q", req.GetQuestionId()))
	}
	store := os.Getenv(fakeProviderKnownHostsEnvVar)
	if store == "" {
		return &contractv1.ConfirmResponse{}, nil
	}
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(store, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if _, err := file.WriteString(fakeHostLine()); err != nil {
		return nil, err
	}
	return &contractv1.ConfirmResponse{}, nil
}

func alreadyRecorded(store string) bool {
	content, err := os.ReadFile(store)
	return err == nil && strings.Contains(string(content), fakeHostLine())
}

func spawnGrandchildSurvivor(keepPipe, ownGroup bool) error {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), fakeProviderEnvVar+"=1", fakeProviderModeEnvVar+"=grandchild-survivor")
	if keepPipe {
		cmd.Stdout = os.Stdout
	}
	if ownGroup {
		childprocess.SetOwnGroup(cmd)
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
