package providerprocess

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/cli/internal/runtrace"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type transport interface {
	Client() (contractv1connect.ProviderServiceClient, error)
	VariableStore() (variablestorev1connect.VariableStoreServiceClient, error)
	Facts() *contractv1.ProviderFacts
	explainLost(err error) error
	isExportingSpans() bool
	Close()
}

type clients struct {
	provider      contractv1connect.ProviderServiceClient
	variableStore variablestorev1connect.VariableStoreServiceClient
}

func newClients(httpClient *http.Client, baseURL string, maxMessageBytes int, interceptors ...connect.Interceptor) clients {
	opts := connect.WithClientOptions(
		connect.WithInterceptors(append(interceptors, callSpanInterceptor{}, validate.NewInterceptor())...),
		connect.WithReadMaxBytes(maxMessageBytes),
	)
	return clients{
		provider:      contractv1connect.NewProviderServiceClient(httpClient, baseURL, opts),
		variableStore: variablestorev1connect.NewVariableStoreServiceClient(httpClient, baseURL, opts),
	}
}

func configure(ctx context.Context, client contractv1connect.ProviderServiceClient, name string, config *contractv1.ProviderConfig, edge *contractv1.EdgeSelection) (*contractv1.ProviderFacts, error) {
	configured, err := client.Configure(ctx, &contractv1.ConfigureRequest{Config: config, Edge: edge})
	switch {
	case err == nil:
		return configured.GetFacts(), nil
	case connect.CodeOf(err) != connect.CodeInvalidArgument:
		return nil, fmt.Errorf("provider: configure the provider session: %w", err)
	}
	if code, named := provider.RefusedCode(err); named && code == refusal.CodeUnknownOption {
		return nil, fmt.Errorf("the config configures provider %q with options it does not accept: %w", name, err)
	}
	return nil, fmt.Errorf("provider %q refuses the config it was given: %w", name, err)
}

type streamCall[Req any] func(contractv1connect.ProviderServiceClient, context.Context, *Req) (*connect.ServerStreamForClient[progressv1.OperationEvent], error)

type onCallEvent func(call runtrace.Call, event *progressv1.OperationEvent)

func newTraceCall(t transport, span *trace.SpanContext) runtrace.Call {
	return runtrace.Call{Span: *span, ExportsSpans: t.isExportingSpans()}
}

func stream[Req any](ctx context.Context, t transport, rpc string, req *Req, call streamCall[Req], onEvent onCallEvent) (*progressv1.OperationResult, error) {
	client, err := t.Client()
	if err != nil {
		return nil, err
	}
	ctx, rpcCall := withCallSlot(ctx)
	events, callErr := call(client, ctx, req)
	return driveStream(t, rpc, events, callErr, func(event *progressv1.OperationEvent) { onEvent(newTraceCall(t, rpcCall), event) })
}

func driveStream(t transport, rpc string, stream *connect.ServerStreamForClient[progressv1.OperationEvent], callErr error, onEvent func(*progressv1.OperationEvent)) (*progressv1.OperationResult, error) {
	if callErr != nil {
		return nil, callError(t, rpc, callErr)
	}
	defer stream.Close()

	refused := false
	for stream.Receive() {
		event := stream.Msg()
		onEvent(event)
		result := event.GetResult()
		refused = refused || result.GetRefused()
		if result != nil && !result.GetRefused() {
			if result.GetSuccess() {
				return result, nil
			}
			return result, failedOperation(result)
		}
	}

	if err := stream.Err(); err != nil {
		return nil, streamError(t, rpc, err, refused)
	}
	return nil, t.explainLost(fmt.Errorf("provider: provider closed the %s stream without a result", rpc))
}

func callError(t transport, rpc string, err error) error {
	if IsCancelled(err) {
		return fmt.Errorf("provider: %s was cancelled: %w", rpc, err)
	}
	return t.explainLost(fmt.Errorf("provider: call %s: %w", rpc, err))
}

func streamError(t transport, rpc string, err error, refused bool) error {
	if IsCancelled(err) {
		return fmt.Errorf("provider: %s was cancelled: %w", rpc, err)
	}
	if _, named := provider.RefusedCode(err); refused || named {
		return fmt.Errorf("provider: call %s: %w", rpc, err)
	}
	if connect.IsWireError(err) {
		return t.explainLost(fmt.Errorf("provider: call %s: %w", rpc, err))
	}
	return t.explainLost(fmt.Errorf("provider: provider connection lost: %w", err))
}

func preflight(ctx context.Context, t transport, req *contractv1.PreflightRequest, onProgress onCallEvent) (*contractv1.PreflightResponse, error) {
	client, err := t.Client()
	if err != nil {
		return nil, err
	}
	ctx, call := withCallSlot(ctx)
	stream, err := client.Preflight(ctx, req)
	if err != nil {
		return nil, callError(t, "Preflight", err)
	}
	defer stream.Close()

	for stream.Receive() {
		event := stream.Msg()
		if response := event.GetResponse(); response != nil {
			return response, nil
		}
		onProgress(newTraceCall(t, call), event.GetProgress())
	}
	if err := stream.Err(); err != nil {
		if connect.IsWireError(err) {
			return nil, err
		}
		return nil, streamError(t, "Preflight", err, false)
	}
	return nil, t.explainLost(errors.New("provider: provider closed the Preflight stream without a response"))
}

func readLogs(ctx context.Context, t transport, req *contractv1.ReadLogsRequest, onResponse func(*contractv1.ReadLogsResponse) error) error {
	client, err := t.Client()
	if err != nil {
		return err
	}
	stream, err := client.ReadLogs(ctx, req)
	if err != nil {
		return callError(t, "ReadLogs", err)
	}
	defer stream.Close()

	for stream.Receive() {
		if err := onResponse(stream.Msg()); err != nil {
			return err
		}
	}
	if err := stream.Err(); err != nil {
		return streamError(t, "ReadLogs", err, false)
	}
	return nil
}

func IsCancelled(err error) bool {
	return errors.Is(err, context.Canceled) || connect.CodeOf(err) == connect.CodeCanceled
}
