package buildproxy_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/buildproxy"
)

var buckets = []provider.BindingType{provider.BindingBucket}

func request(grants ...provider.BindingGrant) provider.BindingProxyRequest {
	return provider.BindingProxyRequest{Slug: "shop", Env: "production", Grants: grants, ReportFailure: func(error) {}}
}

func grant(grantee string, bindings ...provider.Binding) provider.BindingGrant {
	return provider.BindingGrant{Grantee: grantee, Bindings: bindings}
}

func TestARequestBindingNothingTheVendorServesIsAnsweredWithNoProxyAndOpensNothing(t *testing.T) {
	proxy, err := buildproxy.Serve(context.Background(), request(grant("web",
		provider.Binding{Type: provider.BindingTopic, Name: "topic--events"},
		provider.Binding{Type: provider.BindingTask, Name: "task--mail"},
	)), buckets, func(context.Context, []provider.BindingGrant) (provider.BindingProxy, error) {
		t.Error("the vendor's proxy was opened for a build that binds none of what it serves")
		return provider.BindingProxy{}, nil
	})
	if err != nil {
		t.Fatalf("Serve() = %v", err)
	}

	if proxy.Address != "" || len(proxy.Sessions) != 0 || proxy.Close != nil {
		t.Errorf("Serve() = %+v, want no proxy listening and no token minted", proxy)
	}
	if !slices.Equal(proxy.Unserved, []string{"topic--events", "task--mail"}) {
		t.Errorf("Unserved = %v, want both bindings named", proxy.Unserved)
	}
}

func TestTheVendorOpensItsProxyForTheBindingsItServesAndTheRestAreNamedUnserved(t *testing.T) {
	uploads := provider.Binding{Type: provider.BindingBucket, Name: "bucket--uploads", Properties: map[string]string{provider.PropertyBucket: "shop-uploads"}}
	var opened []provider.BindingGrant
	proxy, err := buildproxy.Serve(context.Background(), request(grant("web", uploads, provider.Binding{Type: provider.BindingTopic, Name: "topic--events"})), buckets,
		func(_ context.Context, grants []provider.BindingGrant) (provider.BindingProxy, error) {
			opened = grants
			return provider.BindingProxy{Address: "http://127.0.0.1:41999", Sessions: []provider.BindingSession{{Grantee: "web", SessionToken: "token-1"}}}, nil
		})
	if err != nil {
		t.Fatalf("Serve() = %v", err)
	}

	if len(opened) != 1 || len(opened[0].Bindings) != 1 || opened[0].Bindings[0].Name != "bucket--uploads" {
		t.Errorf("the proxy was opened for %v, want the uploads bucket alone", opened)
	}
	if proxy.Address != "http://127.0.0.1:41999" || len(proxy.Sessions) != 1 || proxy.Sessions[0].SessionToken != "token-1" {
		t.Errorf("Serve() = %+v, want the proxy the vendor opened", proxy)
	}
	if !slices.Equal(proxy.Unserved, []string{"topic--events"}) {
		t.Errorf("Unserved = %v, want the topic", proxy.Unserved)
	}
}

func TestAVendorThatCannotOpenItsProxyFailsIt(t *testing.T) {
	refused := errors.New("the stack is not bootstrapped")
	_, err := buildproxy.Serve(context.Background(), request(grant("web", provider.Binding{Type: provider.BindingBucket, Name: "bucket--uploads"})), buckets,
		func(context.Context, []provider.BindingGrant) (provider.BindingProxy, error) {
			return provider.BindingProxy{}, refused
		})

	if !errors.Is(err, refused) {
		t.Errorf("Serve() = %v, want the vendor's refusal", err)
	}
}

func TestEachGranteeIsOpenedWithOnlyItsOwnServedBindingsAndAGranteeBindingNothingServedIsLeftOut(t *testing.T) {
	uploads := provider.Binding{Type: provider.BindingBucket, Name: "bucket--uploads"}
	assets := provider.Binding{Type: provider.BindingBucket, Name: "bucket--assets"}
	events := provider.Binding{Type: provider.BindingTopic, Name: "topic--events"}
	var opened []provider.BindingGrant
	proxy, err := buildproxy.Serve(context.Background(), request(
		grant("web", uploads, events),
		grant("docs", assets),
		grant("worker", events),
	), buckets, func(_ context.Context, grants []provider.BindingGrant) (provider.BindingProxy, error) {
		opened = grants
		return provider.BindingProxy{}, nil
	})
	if err != nil {
		t.Fatalf("Serve() = %v", err)
	}

	if len(opened) != 2 || opened[0].Grantee != "web" || opened[1].Grantee != "docs" {
		t.Fatalf("opened %+v, want web and docs alone", opened)
	}
	if len(opened[0].Bindings) != 1 || opened[0].Bindings[0].Name != "bucket--uploads" || len(opened[1].Bindings) != 1 || opened[1].Bindings[0].Name != "bucket--assets" {
		t.Errorf("opened %+v, want each grantee its own bucket", opened)
	}
	if !slices.Equal(proxy.Unserved, []string{"topic--events"}) {
		t.Errorf("Unserved = %v, want the topic named once", proxy.Unserved)
	}
}
