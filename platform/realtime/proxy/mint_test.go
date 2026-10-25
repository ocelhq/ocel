package proxy_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/platform/realtime/proxy"
	"github.com/ocelhq/ocel/platform/realtime/token"
)

func mint(service *proxy.Service, grants ...*realtimev1.Grant) (*realtimev1.MintResponse, error) {
	return service.Mint(context.Background(), &realtimev1.MintRequest{Realtime: "app", Subject: "user-1", Ttl: durationpb.New(90 * time.Second), Grants: grants})
}

func newMintService(t *testing.T, properties *bindingsv1.RealtimeProperties) *proxy.Service {
	t.Helper()
	return proxy.NewService(bindRealtime(t, properties), proxy.NewGatewayTransport(http.DefaultClient, "http://127.0.0.1:1/publish"))
}

func TestAMintSignsEachGrantWithTheBindingsKeyForTheChannelItNames(t *testing.T) {
	t.Parallel()

	key := newKey(t)
	before := time.Now().Truncate(time.Second)
	minted, err := mint(newMintService(t, gatewayProperties(key)),
		&realtimev1.Grant{Operation: realtimev1.Operation_OPERATION_CONNECT, Channel: "/app"},
		&realtimev1.Grant{Operation: realtimev1.Operation_OPERATION_SUBSCRIBE, Channel: channel},
		&realtimev1.Grant{Operation: realtimev1.Operation_OPERATION_SUBSCRIBE, Channel: "/app/orders/*"},
	)
	if err != nil {
		t.Fatalf("Mint() = %v", err)
	}
	keys := func(namespace string) (ed25519.PublicKey, bool) {
		return key.Public().(ed25519.PublicKey), namespace == "app"
	}
	wants := []token.Expected{
		{Audience: "realtime.shop.example", Namespace: "app", Operation: token.Connect, Subject: "user-1"},
		{Audience: "realtime.shop.example", Namespace: "app", Operation: token.Subscribe, Channel: channel, Subject: "user-1"},
		{Audience: "realtime.shop.example", Namespace: "app", Operation: token.Subscribe, Channel: "/app/orders/*", Subject: "user-1"},
	}
	if len(minted.GetTokens()) != len(wants) {
		t.Fatalf("Mint() answered %d tokens, want one for each of the %d grants", len(minted.GetTokens()), len(wants))
	}
	for i, want := range wants {
		got := minted.GetTokens()[i]
		claims, err := token.Verify(got.GetToken(), keys, time.Now(), want)
		if err != nil {
			t.Errorf("token %d = %q, which does not verify as %+v: %v", i, got.GetToken(), want, err)
			continue
		}
		if claims.Issuer != "ocel:rt:app" || claims.ExpiresAt-claims.IssuedAt != 90 || claims.ID == "" {
			t.Errorf("token %d claims %+v, want issuer ocel:rt:app, a jti and 90s between iat and exp", i, claims)
		}
		if claims.ExpiresAt != minted.GetExpiresAt().GetSeconds() {
			t.Errorf("token %d expires at %d, want the answer's %v", i, claims.ExpiresAt, minted.GetExpiresAt().AsTime())
		}
	}
	for i, wantChannel := range []string{"/app", channel, "/app/orders/*"} {
		if got := minted.GetTokens()[i].GetChannel(); got != wantChannel {
			t.Errorf("token %d is for channel %q, want %q as asked: this runtime leaves channels as they are", i, got, wantChannel)
		}
	}
	if at := minted.GetExpiresAt().AsTime(); at.Before(before.Add(90*time.Second)) || at.After(time.Now().Add(90*time.Second)) {
		t.Errorf("the answer expires at %v, want 90s after the mint", at)
	}
}

func TestAMintOnAResourceThisRuntimeHasNoBindingForIsAFailedPrecondition(t *testing.T) {
	t.Parallel()

	service := proxy.NewService(records{}, proxy.NewGatewayTransport(http.DefaultClient, "http://127.0.0.1:1/publish"))
	_, err := mint(service, &realtimev1.Grant{Operation: realtimev1.Operation_OPERATION_SUBSCRIBE, Channel: channel})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("Mint() = %v, want a failed precondition: nothing was bound to mint for", err)
	}
}

func TestAMintFromABindingWithoutAnEd25519SigningKeyIsAFailedPrecondition(t *testing.T) {
	t.Parallel()

	properties := gatewayProperties(newKey(t))
	properties.SigningKey = nil
	_, err := mint(newMintService(t, properties), &realtimev1.Grant{Operation: realtimev1.Operation_OPERATION_SUBSCRIBE, Channel: channel})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("Mint() = %v, want a failed precondition: the binding holds no key to sign with", err)
	}
}
