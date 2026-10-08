package buildproxy_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/localrpc"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/buildproxy"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
)

type bearer string

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", localrpc.FormatAuthHeader(string(b)))
	return http.DefaultTransport.RoundTrip(req)
}

var buckets = []provider.BindingType{provider.BindingBucket}

func request(bindings ...provider.Binding) provider.BindingProxyRequest {
	return provider.BindingProxyRequest{Slug: "shop", Env: "production", Bindings: bindings, ReportFailure: func(error) {}}
}

func TestARequestBindingNothingTheVendorServesIsAnsweredWithNoProxyAndOpensNothing(t *testing.T) {
	proxy, err := buildproxy.Serve(context.Background(), request(
		provider.Binding{Type: provider.BindingTopic, Name: "topic--events"},
		provider.Binding{Type: provider.BindingTask, Name: "task--mail"},
	), buckets, func(context.Context, []provider.Binding) (bindingproxy.Services, func(), error) {
		t.Error("the vendor's services were opened for a build that binds none of them")
		return bindingproxy.Services{}, func() {}, nil
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

func TestTheProxyOpensTheVendorsServicesForTheBindingsItServesAndNamesTheRestUnserved(t *testing.T) {
	uploads := provider.Binding{Type: provider.BindingBucket, Name: "bucket--uploads", Properties: map[string]string{provider.PropertyBucket: "shop-uploads"}}
	var opened []provider.Binding
	proxy, err := buildproxy.Serve(context.Background(), request(uploads, provider.Binding{Type: provider.BindingTopic, Name: "topic--events"}), buckets,
		func(_ context.Context, bindings []provider.Binding) (bindingproxy.Services, func(), error) {
			opened = bindings
			return bindingproxy.Services{Buckets: bucketv1connect.UnimplementedBucketServiceHandler{}}, func() {}, nil
		})
	if err != nil {
		t.Fatalf("Serve() = %v", err)
	}
	t.Cleanup(proxy.Close)

	if len(opened) != 1 || opened[0].Name != "bucket--uploads" {
		t.Errorf("the services were opened for %v, want the uploads bucket alone", opened)
	}
	if !slices.Equal(proxy.Unserved, []string{"topic--events"}) {
		t.Errorf("Unserved = %v, want the topic", proxy.Unserved)
	}
	_, err = bucketv1connect.NewBucketServiceClient(&http.Client{Transport: bearer(proxy.SessionToken)}, proxy.Address).
		List(context.Background(), &bucketv1.ListRequest{Bucket: "shop-uploads"})
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("List with the session token = %v, want it to reach the bucket service", err)
	}
}

func TestClosingTheProxyReleasesWhatTheVendorOpenedAndReportsNoFailure(t *testing.T) {
	released := false
	var reported []error
	req := request(provider.Binding{Type: provider.BindingBucket, Name: "bucket--uploads"})
	req.ReportFailure = func(err error) { reported = append(reported, err) }
	proxy, err := buildproxy.Serve(context.Background(), req, buckets, func(context.Context, []provider.Binding) (bindingproxy.Services, func(), error) {
		return bindingproxy.Services{Buckets: bucketv1connect.UnimplementedBucketServiceHandler{}}, func() { released = true }, nil
	})
	if err != nil {
		t.Fatalf("Serve() = %v", err)
	}

	proxy.Close()

	if !released {
		t.Error("closing the proxy left open what the vendor opened for it")
	}
	if len(reported) != 0 {
		t.Errorf("closing the proxy reported %v, want nothing: a deliberate close is no failure", reported)
	}
}

func TestAVendorThatCannotOpenItsServicesFailsTheProxy(t *testing.T) {
	refused := errors.New("the stack is not bootstrapped")
	_, err := buildproxy.Serve(context.Background(), request(provider.Binding{Type: provider.BindingBucket, Name: "bucket--uploads"}), buckets,
		func(context.Context, []provider.Binding) (bindingproxy.Services, func(), error) {
			return bindingproxy.Services{}, nil, refused
		})

	if !errors.Is(err, refused) {
		t.Errorf("Serve() = %v, want the vendor's refusal", err)
	}
}
