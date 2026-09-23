package sdkversion

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/constants"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
)

func TestCompatible(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cli, sdk string
		want     bool
	}{
		{"0.0.x demands the same version", "0.0.3", "0.0.3", true},
		{"0.0.x refuses another patch", "0.0.3", "0.0.4", false},
		{"0.0.x refuses an older patch", "0.0.4", "0.0.3", false},
		{"0.0.x refuses its own release candidate", "0.0.3", "0.0.3-rc.1", false},
		{"a release candidate matches itself", "0.0.3-rc.1", "0.0.3-rc.1", true},
		{"a release candidate matches its PEP 440 spelling", "0.0.3-rc.2", "0.0.3rc2", true},
		{"a release candidate refuses another", "0.0.3-rc.2", "0.0.3rc1", false},
		{"a nightly matches its PEP 440 spelling", "0.0.3-0.nightly.20260923.gabc1234", "0.0.3.dev20260923", true},
		{"a nightly refuses another night", "0.0.3-0.nightly.20260923.gabc1234", "0.0.3.dev20260924", false},
		{"a v prefix is the same version", "0.0.3", "v0.0.3", true},
		{"0.x accepts any patch of its minor", "0.2.0", "0.2.7", true},
		{"0.x accepts a release candidate of its minor", "0.2.1", "0.2.0-rc.3", true},
		{"0.x refuses another minor", "0.2.0", "0.3.0", false},
		{"0.x refuses 1.x", "0.2.0", "1.2.0", false},
		{"1.x accepts any minor of its major", "1.4.0", "1.0.2", true},
		{"1.x refuses another major", "1.4.0", "2.0.0", false},
		{"1.x refuses 0.x", "1.0.0", "0.9.0", false},
		{"a dev CLI checks nothing", "dev", "0.3.0", true},
		{"an unreleased CLI checks nothing", "0.0.0", "0.3.0", true},
		{"an unreleased SDK is not checked", "0.3.0", "0.0.0", true},
		{"a devel Go build is not checked", "0.3.0", "(devel)", true},
		{"a dev SDK is not checked", "0.3.0", "dev", true},
		{"a missing SDK version is not checked", "0.3.0", "", true},
		{"a Go pseudo-version is not checked", "0.3.0", "v0.0.0-20260923120000-abcdef123456", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Compatible(c.cli, c.sdk); got != c.want {
				t.Errorf("Compatible(%q, %q) = %v, want %v", c.cli, c.sdk, got, c.want)
			}
		})
	}
}

func TestUpgradeNamesEachEcosystemsCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		language, cli, want string
	}{
		{JS, "0.2.0", "npm i ocel@0.2.0"},
		{Python, "0.2.0", "uv add ocel==0.2.0"},
		{Python, "0.2.0-rc.1", "uv add ocel==0.2.0rc1"},
		{Python, "0.2.0-0.nightly.20260923.gabc1234", "uv add ocel==0.2.0.dev20260923"},
		{Rust, "0.2.0", "cargo add ocel-sdk@0.2.0"},
		{Go, "0.2.0", "go get ocel.dev@v0.2.0"},
		{"cobol", "0.2.0", ""},
	}
	for _, c := range cases {
		if got := Upgrade(c.language, c.cli); got != c.want {
			t.Errorf("Upgrade(%q, %q) = %q, want %q", c.language, c.cli, got, c.want)
		}
	}
}

func TestMismatchNamesBothVersionsTheLanguageAndTheFix(t *testing.T) {
	t.Parallel()

	err := Check(Python, "0.0.2", "0.0.3")
	want := "the Python SDK (ocel) is version 0.0.2 and this CLI is version 0.0.3; an SDK works with the CLI of its own release — run `uv add ocel==0.0.3`"
	if err == nil || err.Error() != want {
		t.Fatalf("Check() = %v, want %q", err, want)
	}
}

type collector struct {
	resourcesv1connect.UnimplementedResourceServiceHandler
}

func (collector) Declare(context.Context, *resourcesv1.DeclareRequest) (*resourcesv1.DeclareResponse, error) {
	return &resourcesv1.DeclareResponse{}, nil
}

func TestTheGateRefusesAMismatchedSDKAndHoldsTheRefusal(t *testing.T) {
	t.Parallel()

	gate := NewGate("0.0.3")
	mux := http.NewServeMux()
	mux.Handle(resourcesv1connect.NewResourceServiceHandler(collector{}, connect.WithInterceptors(gate.Interceptor())))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	declare := func(header string) error {
		client := resourcesv1connect.NewResourceServiceClient(server.Client(), server.URL,
			connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					if header != "" {
						req.Header().Set(constants.SDKVersionHeader, header)
					}
					return next(ctx, req)
				}
			})))
		_, err := client.Declare(context.Background(), &resourcesv1.DeclareRequest{})
		return err
	}

	if err := declare(""); err != nil {
		t.Fatalf("an SDK that names no version was refused: %v", err)
	}
	if err := declare("rust/0.0.3"); err != nil {
		t.Fatalf("a matching SDK was refused: %v", err)
	}
	if held := gate.Take(); held != nil {
		t.Fatalf("Take() = %v after only compatible calls", held)
	}

	err := declare("js/0.0.2")
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a mismatched SDK got %v, want failed_precondition", err)
	}
	if !strings.Contains(err.Error(), "npm i ocel@0.0.3") {
		t.Fatalf("the refusal %q names no upgrade", err)
	}

	var mismatch *MismatchError
	if held := gate.Take(); !errors.As(held, &mismatch) || mismatch.Language != JS || mismatch.SDK != "0.0.2" {
		t.Fatalf("Take() = %v, want the js mismatch", held)
	}
	if held := gate.Take(); held != nil {
		t.Fatalf("Take() = %v after the refusal was taken", held)
	}
}
