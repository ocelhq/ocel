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

func request(bindings ...provider.Binding) provider.BindingProxyRequest {
	return provider.BindingProxyRequest{Slug: "shop", Env: "production", Bindings: bindings, ReportFailure: func(error) {}}
}

func TestARequestBindingNothingTheVendorServesIsAnsweredWithNoProxyAndOpensNothing(t *testing.T) {
	proxy, err := buildproxy.Serve(context.Background(), request(
		provider.Binding{Type: provider.BindingTopic, Name: "topic--events"},
		provider.Binding{Type: provider.BindingTask, Name: "task--mail"},
	), buckets, func(context.Context, []provider.Binding) (provider.BindingProxy, error) {
		t.Error("the vendor's proxy was opened for a build that binds none of what it serves")
		return provider.BindingProxy{}, nil
	})
	if err != nil {
		t.Fatalf("Serve() = %v", err)
	}

	if proxy.Address != "" || proxy.SessionToken != "" || proxy.Close != nil {
		t.Errorf("Serve() = %+v, want no proxy listening and no token minted", proxy)
	}
	if !slices.Equal(proxy.Unserved, []string{"topic--events", "task--mail"}) {
		t.Errorf("Unserved = %v, want both bindings named", proxy.Unserved)
	}
}

func TestTheVendorOpensItsProxyForTheBindingsItServesAndTheRestAreNamedUnserved(t *testing.T) {
	uploads := provider.Binding{Type: provider.BindingBucket, Name: "bucket--uploads", Properties: map[string]string{provider.PropertyBucket: "shop-uploads"}}
	var opened []provider.Binding
	proxy, err := buildproxy.Serve(context.Background(), request(uploads, provider.Binding{Type: provider.BindingTopic, Name: "topic--events"}), buckets,
		func(_ context.Context, bindings []provider.Binding) (provider.BindingProxy, error) {
			opened = bindings
			return provider.BindingProxy{Address: "http://127.0.0.1:41999", SessionToken: "token-1"}, nil
		})
	if err != nil {
		t.Fatalf("Serve() = %v", err)
	}

	if len(opened) != 1 || opened[0].Name != "bucket--uploads" {
		t.Errorf("the proxy was opened for %v, want the uploads bucket alone", opened)
	}
	if proxy.Address != "http://127.0.0.1:41999" || proxy.SessionToken != "token-1" {
		t.Errorf("Serve() = %+v, want the proxy the vendor opened", proxy)
	}
	if !slices.Equal(proxy.Unserved, []string{"topic--events"}) {
		t.Errorf("Unserved = %v, want the topic", proxy.Unserved)
	}
}

func TestAVendorThatCannotOpenItsProxyFailsIt(t *testing.T) {
	refused := errors.New("the stack is not bootstrapped")
	_, err := buildproxy.Serve(context.Background(), request(provider.Binding{Type: provider.BindingBucket, Name: "bucket--uploads"}), buckets,
		func(context.Context, []provider.Binding) (provider.BindingProxy, error) {
			return provider.BindingProxy{}, refused
		})

	if !errors.Is(err, refused) {
		t.Errorf("Serve() = %v, want the vendor's refusal", err)
	}
}
