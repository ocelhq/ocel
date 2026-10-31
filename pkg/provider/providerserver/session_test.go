package providerserver_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

type goAheadProvider struct {
	provider.Provider
	confirmed *int
}

func (p goAheadProvider) Credentials() provider.Credentials {
	return goAheadCredentials{Credentials: p.Provider.Credentials(), confirmed: p.confirmed}
}

type goAheadCredentials struct {
	provider.Credentials
	confirmed *int
}

func (c goAheadCredentials) Whoami(ctx context.Context) (provider.Principal, error) {
	if *c.confirmed > 0 {
		return c.Credentials.Whoami(ctx)
	}
	return provider.Principal{}, provider.Ask("nobody said to go ahead", provider.Question{
		Finding: "nobody said to go ahead",
		Prompt:  "Go ahead?",
		Remedy:  "say to go ahead",
		Confirm: func(context.Context) error { *c.confirmed++; return nil },
	})
}

func TestASessionIsConfiguredAndConfirmedInProcessAndListsTheQuestionsItAsked(t *testing.T) {
	t.Parallel()
	confirmed := 0
	session := providerserver.NewSession(providerserver.Config{
		Version: "1.0.0",
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return goAheadProvider{Provider: fake.NewProvider(fake.Options{}), confirmed: &confirmed}, nil
		},
	})
	ctx := context.Background()
	facts, err := session.Configure(ctx, provider.Settings{ProjectDir: t.TempDir()}, nil)
	if err != nil || facts == nil {
		t.Fatalf("Configure() = (%v, %v), want the provider's facts", facts, err)
	}
	if err := session.Confirm(ctx, "never-asked"); !errors.Is(err, providerserver.ErrQuestionNotPending) {
		t.Fatalf("Confirm() of a question never asked = %v, want ErrQuestionNotPending", err)
	}

	server := httptest.NewServer(session.Handler())
	t.Cleanup(server.Close)
	stream, err := contractv1connect.NewProviderServiceClient(server.Client(), server.URL).Preflight(ctx, &contractv1.PreflightRequest{})
	if err == nil {
		for stream.Receive() {
		}
		err = stream.Err()
		stream.Close()
	}
	var wire *connect.Error
	if !errors.As(err, &wire) {
		t.Fatalf("Preflight() = %v, want it to ask a question", err)
	}
	pending := session.ListPendingQuestions()
	if len(pending) != 1 || pending[0].AskedAt.IsZero() {
		t.Fatalf("ListPendingQuestions() = %+v, want the one question Preflight asked", pending)
	}
	if err := session.Confirm(ctx, pending[0].ID); err != nil || confirmed != 1 {
		t.Fatalf("Confirm() = %v with %d confirmations, want the question confirmed once", err, confirmed)
	}
	if left := session.ListPendingQuestions(); len(left) != 0 {
		t.Errorf("ListPendingQuestions() after the confirm = %+v, want none", left)
	}
}
