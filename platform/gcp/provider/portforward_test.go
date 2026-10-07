package gcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/relay"
)

func TestAGCPProviderForwardsPorts(t *testing.T) {
	t.Parallel()

	p, err := NewProvider(Options{Project: "acme-prod", Region: "europe-west1"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Hooks().ForwardPorts == nil {
		t.Error("Hooks().ForwardPorts = nil, so a build gets no bindings to the databases and caches on the tier's network")
	}
}

func TestForwardingNoBindingTouchesNoCloud(t *testing.T) {
	t.Parallel()

	forwards, err := (&Provider{}).ForwardPorts(context.Background(), provider.PortForwardRequest{})

	if err != nil || len(forwards) != 0 {
		t.Errorf("ForwardPorts() of no binding = %v, %v, want nothing forwarded and nothing reached", forwards, err)
	}
}

func TestABindingThatNamesNoHostAndPortIsRefusedBeforeAnyBastionIsMade(t *testing.T) {
	t.Parallel()
	server := &runServer{}
	p := server.open(t)

	forwards, err := p.ForwardPorts(context.Background(), provider.PortForwardRequest{
		Tier:     "production",
		Bindings: []provider.Binding{{Type: provider.BindingPostgres, Name: "orders", Properties: map[string]string{provider.PropertyHost: "10.240.0.5"}}},
	})

	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid || len(forwards) != 0 {
		t.Fatalf("ForwardPorts() = %v, %v, want an invalid refusal naming the binding", forwards, err)
	}
	if server.serving() != nil {
		t.Error("a bastion was made for a binding no port can be forwarded to")
	}
}

func TestOneIdentityTokenServesEveryConnectionOfABuildUntilItIsOldEnoughToRenew(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	var minted []string
	tokens := &identityTokens{
		audience: "ocel-production-bastion",
		now:      func() time.Time { return now },
		prove: func(_ context.Context, audience string) (envsource.IdentityProof, error) {
			minted = append(minted, audience)
			return envsource.IdentityProof{IDToken: "token-" + string(rune('a'+len(minted)-1))}, nil
		},
	}

	first, _ := tokens.mint(context.Background())
	now = now.Add(identityTokenLife - time.Minute)
	again, _ := tokens.mint(context.Background())
	now = now.Add(2 * time.Minute)
	renewed, _ := tokens.mint(context.Background())

	if first != "token-a" || again != "token-a" || renewed != "token-b" {
		t.Errorf("tokens = %q, %q, %q, want one token reused until it is older than %s", first, again, renewed, identityTokenLife)
	}
	if len(minted) != 2 || minted[0] != "ocel-production-bastion" {
		t.Errorf("tokens were minted for %v, want two, each for the bastion's own audience", minted)
	}
}

func TestAForwardIsOpenedAgainWhileCloudRunStillRefusesAnInvokerGrantItJustMade(t *testing.T) {
	t.Parallel()
	var tries int
	b := bastion{
		waited: func(context.Context, int) bool { return true },
		open: func(context.Context, relay.Link) (*relay.Forward, error) {
			tries++
			if tries < 3 {
				return nil, refusal.Refuse(refusal.CodeNotReady, "the bastion refused the connection (HTTP 403)")
			}
			return &relay.Forward{}, nil
		},
	}

	forward, err := b.openForward(context.Background(), relay.Link{})

	if err != nil || forward == nil || tries != 3 {
		t.Errorf("openForward() = %v, %v after %d tries, want the forward on the third", forward, err, tries)
	}
}

func TestAForwardThatNeverComesReadyIsGivenUpOnAfterABoundedNumberOfTries(t *testing.T) {
	t.Parallel()
	var tries int
	b := bastion{
		waited: func(context.Context, int) bool { return true },
		open: func(context.Context, relay.Link) (*relay.Forward, error) {
			tries++
			return nil, refusal.Refuse(refusal.CodeNotReady, "the bastion refused the connection")
		},
	}

	_, err := b.openForward(context.Background(), relay.Link{})

	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeNotReady || tries != forwardOpenAttempts {
		t.Errorf("openForward() = %v after %d tries, want a not-ready refusal after %d", err, tries, forwardOpenAttempts)
	}
}

func TestAFailureThatWaitingCannotMendIsNotRetried(t *testing.T) {
	t.Parallel()
	var tries int
	broken := errors.New("mint an identity token: the credential is a user's own login")
	b := bastion{
		waited: func(context.Context, int) bool { return true },
		open: func(context.Context, relay.Link) (*relay.Forward, error) {
			tries++
			return nil, broken
		},
	}

	_, err := b.openForward(context.Background(), relay.Link{})

	if !errors.Is(err, broken) || tries != 1 {
		t.Errorf("openForward() = %v after %d tries, want the failure after one", err, tries)
	}
}
