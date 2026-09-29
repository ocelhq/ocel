package clitest

import (
	"context"
	"encoding/hex"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func fakeProviderClient(t *testing.T) contractv1connect.ProviderServiceClient {
	t.Helper()
	srv := httptest.NewServer(fakeProviderRoutes(&deployFakeProviderServer{}))
	t.Cleanup(srv.Close)
	return contractv1connect.NewProviderServiceClient(srv.Client(), srv.URL)
}

func TestEveryScopeTheFakeProviderOpensEndsBeforeItsResult(t *testing.T) {
	t.Parallel()

	client := fakeProviderClient(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func() (*connect.ServerStreamForClient[progressv1.OperationEvent], error)
	}{
		{"a deploy the manifest refuses", func() (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return client.Deploy(ctx, &contractv1.DeployRequest{})
		}},
		{"a dry deploy", func() (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return client.Deploy(ctx, &contractv1.DeployRequest{Dry: true, Manifest: &contractv1.Manifest{SchemaVersion: "1"}})
		}},
		{"a promotion prune", func() (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return client.RemoveStalePromotions(ctx, &contractv1.RemoveStalePromotionsRequest{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream, err := tc.call()
			if err != nil {
				t.Fatalf("call error = %v", err)
			}
			defer stream.Close()

			open := map[string]bool{}
			var started, resulted bool
			for stream.Receive() {
				ev := stream.Msg()
				id := hex.EncodeToString(ev.GetSpanId())
				switch {
				case ev.GetStarted() != nil:
					started, open[id] = true, true
				case ev.GetEnded() != nil:
					delete(open, id)
				case ev.GetResult() != nil:
					resulted = true
					if len(open) > 0 {
						t.Errorf("the result came with %d scopes still open, want every scope the fake started ended first", len(open))
					}
				}
			}
			if err := stream.Err(); err != nil {
				t.Fatalf("stream error = %v", err)
			}
			if !started || !resulted {
				t.Fatalf("started = %v, resulted = %v, want the call to open scopes and end in a result", started, resulted)
			}
		})
	}
}

func TestAFailedResultIsSaidAtErrorInTheInnermostOpenScopeBeforeItEndsLikeTheRealProvider(t *testing.T) {
	t.Parallel()

	stream, err := fakeProviderClient(t).Deploy(context.Background(), &contractv1.DeployRequest{})
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}
	defer stream.Close()

	var said *progressv1.OperationEvent
	var failure string
	for stream.Receive() {
		ev := stream.Msg()
		switch {
		case ev.GetBody() == nil && ev.GetLevel() == progressv1.Level_LEVEL_ERROR:
			said = ev
		case ev.GetEnded() != nil && ev.GetEnded().GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR:
			if failure != "" {
				continue
			}
			if said == nil || hex.EncodeToString(said.GetSpanId()) != hex.EncodeToString(ev.GetSpanId()) {
				t.Fatalf("the innermost scope ended in error before its failure was said in it")
			}
			failure = said.GetMessage()
		case ev.GetResult() != nil:
			if want := ev.GetResult().GetError(); failure != want {
				t.Errorf("the failure said is %q, want the result's error %q", failure, want)
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error = %v", err)
	}
	if failure == "" {
		t.Fatal("no scope ended in error, want the refused deploy to fail its open scope")
	}
}
