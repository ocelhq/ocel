package server

import (
	"context"
	"errors"
	"testing"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	contractv1connect "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
)

func refusedCode(t *testing.T, err error) connect.Code {
	t.Helper()
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("err = %v, want a connect error", err)
	}
	return connectErr.Code()
}

func TestContractRefusesMalformedRequests(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		call func(contractv1connect.ProviderServiceClient) error
	}{
		{"a tag carrying a path separator", func(c contractv1connect.ProviderServiceClient) error {
			stream, err := c.Deploy(context.Background(), &contractv1.DeployRequest{
				Manifest: wellFormedManifest(),
				Tag:      "feature/x",
			})
			if err != nil {
				return err
			}
			_, err = drainStream(stream)
			return err
		}},
		{"a slug that would open a second segment of the parameter path", func(c contractv1connect.ProviderServiceClient) error {
			manifest := wellFormedManifest()
			manifest.Slug = "acme/../root"
			stream, err := c.Deploy(context.Background(), &contractv1.DeployRequest{Manifest: manifest})
			if err != nil {
				return err
			}
			_, err = drainStream(stream)
			return err
		}},
		{"a prune keeping a negative number of promotions", func(c contractv1connect.ProviderServiceClient) error {
			stream, err := c.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{Slug: "acme", KeepN: -1})
			if err != nil {
				return err
			}
			_, err = drainStream(stream)
			return err
		}},
		{"a global preview domain asked for under the production class", func(c contractv1connect.ProviderServiceClient) error {
			_, err := c.GetPreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{
				Tier: environmentv1.Tier_TIER_PRODUCTION,
			})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			t.Parallel()
			if got := refusedCode(t, tc.call(newTestClient(t, testToken))); got != connect.CodeInvalidArgument {
				t.Fatalf("code = %v, want %v", got, connect.CodeInvalidArgument)
			}
		})
	}
}

func TestVarStoreRefusesMalformedRequests(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		call func(envvarsv1connect.EnvVarsServiceClient) error
	}{
		{"a link published to no class at all", func(c envvarsv1connect.EnvVarsServiceClient) error {
			_, err := c.ListLinks(context.Background(), &envvarsv1.ListLinksRequest{Slug: "acme"})
			return err
		}},
		{"a link set request carrying no link", func(c envvarsv1connect.EnvVarsServiceClient) error {
			_, err := c.SetLink(context.Background(), &envvarsv1.SetLinkRequest{
				Slug: "acme",
				Tier: environmentv1.Tier_TIER_PRODUCTION,
			})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			t.Parallel()
			if got := refusedCode(t, tc.call(newTestVarsClient(t, testToken))); got != connect.CodeInvalidArgument {
				t.Fatalf("code = %v, want %v", got, connect.CodeInvalidArgument)
			}
		})
	}
}

func TestCoordinateRefusesUnusableComponents(t *testing.T) {
	t.Parallel()

	cases := map[string]*envvarsv1.Coordinate{
		"a slug carrying the store key delimiter":           {Slug: "sh#op", Key: "STRIPE_KEY"},
		"a variable name carrying the store delimiter":      {Slug: "shop", Key: "STRIPE#KEY"},
		"a folder carrying the store key delimiter":         {Slug: "shop", Key: "K", Folder: "/we#b"},
		"an environment carrying the store delimiter":       {Slug: "shop", Key: "K", Environment: "sta#ging"},
		"the class-wide sentinel named as environment":      {Slug: "shop", Key: "K", Environment: "*"},
		"a folder that is not anchored at the root":         {Slug: "shop", Key: "K", Folder: "web"},
		"a folder spelled with a trailing separator":        {Slug: "shop", Key: "K", Folder: "/web/"},
		"a folder with an empty path segment":               {Slug: "shop", Key: "K", Folder: "//web"},
		"the root spelled as a folder rather than left off": {Slug: "shop", Key: "K", Folder: "/"},
		"no project slug":  {Key: "K"},
		"no variable name": {Slug: "shop"},
	}
	client := newTestVarsClient(t, testToken)
	for name, coordinate := range cases {
		t.Run(name+" is refused", func(t *testing.T) {
			t.Parallel()
			_, err := client.ListVersions(context.Background(), &envvarsv1.ListVersionsRequest{
				Tier:       environmentv1.Tier_TIER_PRODUCTION,
				Coordinate: coordinate,
			})
			if got := refusedCode(t, err); got != connect.CodeInvalidArgument {
				t.Fatalf("code = %v, want %v", got, connect.CodeInvalidArgument)
			}
		})
	}
}

func TestCoordinateAcceptsAFolderedCell(t *testing.T) {
	t.Parallel()

	_, err := newTestVarsClient(t, testToken).ListVersions(context.Background(), &envvarsv1.ListVersionsRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &envvarsv1.Coordinate{Slug: "shop", Key: "STRIPE_KEY", Folder: "/web/admin", Environment: "staging"},
	})
	var connectErr *connect.Error
	if errors.As(err, &connectErr) && connectErr.Code() == connect.CodeInvalidArgument {
		t.Fatalf("ListVersions() err = %v, want a well formed coordinate admitted", err)
	}
}
