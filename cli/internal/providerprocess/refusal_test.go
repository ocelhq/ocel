package providerprocess_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type refusingProvider struct {
	contractv1connect.UnimplementedProviderServiceHandler
	refused error
}

func (refusingProvider) Configure(context.Context, *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	return &contractv1.ConfigureResponse{Facts: &contractv1.ProviderFacts{}}, nil
}

func (r refusingProvider) ListEnvironments(context.Context, *contractv1.ListEnvironmentsRequest) (*contractv1.ListEnvironmentsResponse, error) {
	return nil, r.refused
}

func (r refusingProvider) RemoveEnvironment(_ context.Context, _ *contractv1.RemoveEnvironmentRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	code := ""
	if c := connect.CodeOf(r.refused); c != connect.CodeInternal {
		code = c.String()
	}
	return stream.Send(&progressv1.OperationEvent{Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{
		Error:       r.refused.Error(),
		ConnectCode: code,
	}}})
}

func refusedBy(t *testing.T, refused error) *providerprocess.Provider {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(contractv1connect.NewProviderServiceHandler(refusingProvider{refused: refused}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	ctx, span := remoteSpan(t)
	p, err := providerprocess.StartRemote(ctx, remoteProject(t), span, providerprocess.Questions{}, providerprocess.Remote{Name: "ocel", URL: server.URL, Tokens: &issuedTokens{tokens: []string{"token"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestAProviderRefusalFailsWithTheCodeOfItsKindInTheProvidersOwnWords(t *testing.T) {
	const words = "cloud.preview_limit: the Free plan runs 2 preview environments of a project at once"
	for name, tc := range map[string]struct {
		refused   error
		code      string
		retryable bool
		wire      string
	}{
		"denied":      {refused: provider.RefusalError(refusal.Refuse(refusal.CodeDenied, words)), code: clierror.CodeProviderDenied, wire: "permission_denied"},
		"not ready":   {refused: provider.RefusalError(refusal.Refuse(refusal.CodeNotReady, words)), code: clierror.CodeProviderNotReady, wire: "failed_precondition"},
		"busy":        {refused: provider.RefusalError(refusal.Refuse(refusal.CodeBusy, words)), code: clierror.CodeProviderBusy, retryable: true, wire: "aborted"},
		"invalid":     {refused: provider.RefusalError(refusal.Refuse(refusal.CodeInvalid, words)), code: clierror.CodeProviderInvalidRequest, wire: "invalid_argument"},
		"unsupported": {refused: provider.RefusalError(refusal.Refuse(refusal.CodeUnsupported, words)), code: clierror.CodeProviderUnsupported, wire: "unimplemented"},
		"not found":   {refused: connect.NewError(connect.CodeNotFound, errors.New(words)), code: clierror.CodeProviderNotFound, wire: "not_found"},
		"exhausted":   {refused: connect.NewError(connect.CodeResourceExhausted, errors.New(words)), code: clierror.CodeProviderBusy, retryable: true, wire: "resource_exhausted"},
		"unavailable": {refused: connect.NewError(connect.CodeUnavailable, errors.New(words)), code: clierror.CodeProviderUnavailable, retryable: true, wire: "unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			p := refusedBy(t, tc.refused)
			ctx, _ := remoteSpan(t)
			for call, err := range map[string]error{
				"unary": listEnvironments(ctx, p),
				"streamed": func() error {
					_, err := providerprocess.Stream(ctx, p, "RemoveEnvironment", &contractv1.RemoveEnvironmentRequest{Slug: "shop", Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "feat-x"}}, contractv1connect.ProviderServiceClient.RemoveEnvironment)
					return err
				}(),
			} {
				got := clierror.NewRunError(err)
				if got.GetCode() != tc.code || got.GetRetryable() != tc.retryable {
					t.Errorf("%s: code = %q retryable %v, want %q retryable %v (err %v)", call, got.GetCode(), got.GetRetryable(), tc.code, tc.retryable, err)
				}
				if !strings.Contains(got.GetMessage(), words) || strings.Contains(got.GetMessage(), tc.wire) {
					t.Errorf("%s: message = %q, want the provider's words without %s", call, got.GetMessage(), tc.wire)
				}
				if connect.CodeOf(err).String() != tc.wire && call == "unary" {
					t.Errorf("%s: wire code = %v, want %s still readable", call, connect.CodeOf(err), tc.wire)
				}
			}
		})
	}
}

func TestAProviderFailingInternallyStaysAnInternalError(t *testing.T) {
	p := refusedBy(t, connect.NewError(connect.CodeInternal, errors.New("the provider broke")))
	ctx, _ := remoteSpan(t)
	if got := clierror.NewRunError(listEnvironments(ctx, p)); got.GetCode() != "internal" {
		t.Fatalf("code = %q, want internal", got.GetCode())
	}
	_, err := providerprocess.Stream(ctx, p, "RemoveEnvironment", &contractv1.RemoveEnvironmentRequest{Slug: "shop", Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "feat-x"}}, contractv1connect.ProviderServiceClient.RemoveEnvironment)
	if got := clierror.NewRunError(err); got.GetCode() != "internal" || !strings.Contains(got.GetMessage(), "the provider broke") {
		t.Fatalf("streamed = %v, want internal in the provider's words", got)
	}
}
