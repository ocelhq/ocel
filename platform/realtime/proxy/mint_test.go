package proxy_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/platform/realtime/proxy"
	"github.com/ocelhq/ocel/platform/realtime/token"
)

func mint(service *proxy.Service, isConnect bool, subscribeChannels ...string) (*realtimev1.MintResponse, error) {
	return service.Mint(context.Background(), &realtimev1.MintRequest{Realtime: "app", Subject: "user-1", Ttl: durationpb.New(90 * time.Second), Connect: isConnect, SubscribeChannels: subscribeChannels})
}

func newMintService(t *testing.T, properties *bindingsv1.RealtimeProperties) *proxy.Service {
	t.Helper()
	return proxy.NewService(bindRealtime(t, properties), proxy.NewGatewayTransport(http.DefaultClient, "http://127.0.0.1:1/publish"))
}

func TestAMintSignsTheConnectTokenAndASubscribeTokenForEachChannelWithTheBindingsKey(t *testing.T) {
	t.Parallel()

	key := newKey(t)
	before := time.Now().Truncate(time.Second)
	minted, err := mint(newMintService(t, gatewayProperties(key)), true, channel, "/app/orders/*")
	if err != nil {
		t.Fatalf("Mint() = %v", err)
	}
	keys := func(namespace string) (ed25519.PublicKey, bool) {
		return key.Public().(ed25519.PublicKey), namespace == "app"
	}
	if len(minted.GetSubscribeTokens()) != 2 {
		t.Fatalf("Mint() answered %d subscribe tokens, want one for each of the 2 channels", len(minted.GetSubscribeTokens()))
	}
	signed := []struct {
		token string
		want  token.Expected
	}{
		{minted.GetConnectToken(), token.Expected{Audience: "realtime.shop.example", Namespace: "app", Operation: token.Connect, Subject: "user-1"}},
		{minted.GetSubscribeTokens()[0].GetToken(), token.Expected{Audience: "realtime.shop.example", Namespace: "app", Operation: token.Subscribe, Channel: channel, Subject: "user-1"}},
		{minted.GetSubscribeTokens()[1].GetToken(), token.Expected{Audience: "realtime.shop.example", Namespace: "app", Operation: token.Subscribe, Channel: "/app/orders/*", Subject: "user-1"}},
	}
	for i, one := range signed {
		claims, err := token.Verify(one.token, keys, time.Now(), one.want)
		if err != nil {
			t.Errorf("token %d = %q, which does not verify as %+v: %v", i, one.token, one.want, err)
			continue
		}
		if claims.Issuer != "ocel:rt:app" || claims.ExpiresAt-claims.IssuedAt != 90 || claims.ID == "" {
			t.Errorf("token %d claims %+v, want issuer ocel:rt:app, a jti and 90s between iat and exp", i, claims)
		}
		if claims.ExpiresAt != minted.GetExpiresAt().GetSeconds() {
			t.Errorf("token %d expires at %d, want the answer's %v", i, claims.ExpiresAt, minted.GetExpiresAt().AsTime())
		}
	}
	for i, wantChannel := range []string{channel, "/app/orders/*"} {
		if got := minted.GetSubscribeTokens()[i].GetChannel(); got != wantChannel {
			t.Errorf("subscribe token %d is for channel %q, want %q as asked: this runtime leaves channels as they are", i, got, wantChannel)
		}
	}
	if at := minted.GetExpiresAt().AsTime(); at.Before(before.Add(90*time.Second)) || at.After(time.Now().Add(90*time.Second)) {
		t.Errorf("the answer expires at %v, want 90s after the mint", at)
	}
}

func TestAMintThatAsksNoConnectAnswersNoConnectToken(t *testing.T) {
	t.Parallel()

	minted, err := mint(newMintService(t, gatewayProperties(newKey(t))), false, channel)
	if err != nil {
		t.Fatalf("Mint() = %v", err)
	}
	if minted.GetConnectToken() != "" || len(minted.GetSubscribeTokens()) != 1 {
		t.Errorf("Mint() = connect token %q and %d subscribe tokens, want only the one subscribe token", minted.GetConnectToken(), len(minted.GetSubscribeTokens()))
	}
}

func TestAMintOnAResourceThisRuntimeHasNoBindingForIsAFailedPrecondition(t *testing.T) {
	t.Parallel()

	service := proxy.NewService(records{}, proxy.NewGatewayTransport(http.DefaultClient, "http://127.0.0.1:1/publish"))
	_, err := mint(service, false, channel)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || strings.Contains(err.Error(), "publish") {
		t.Errorf("Mint() = %v, want a failed precondition that names no publish: nothing was bound to mint for", err)
	}
}

func TestAMintFromABindingWithoutAnEd25519SigningKeyIsAFailedPrecondition(t *testing.T) {
	t.Parallel()

	properties := gatewayProperties(newKey(t))
	properties.SigningKey = nil
	_, err := mint(newMintService(t, properties), false, channel)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("Mint() = %v, want a failed precondition: the binding holds no key to sign with", err)
	}
}
