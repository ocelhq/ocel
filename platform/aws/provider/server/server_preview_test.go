package server

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func TestGlobalPreviewProblem(t *testing.T) {
	t.Parallel()

	recorded := bootstrap.PreviewDomain{BaseDomain: "previews.ocel.dev", Edge: cloudflare.Kind, Scope: "cf-owner"}

	cases := []struct {
		name    string
		req     *deploymentsv1.PreflightRequest
		wantErr bool
	}{
		{
			name:    "a preview deploy that would serve on the shared wildcard refuses",
			req:     &deploymentsv1.PreflightRequest{Slug: "acme"},
			wantErr: true,
		},
		{
			name: "a preview deploy of a project with its own preview domain proceeds",
			req:  &deploymentsv1.PreflightRequest{Slug: "acme", Domains: []string{"*.preview.acme.com"}},
		},
		{
			name: "`ocel preview rm`, which names no project and serves nothing, proceeds",
			req:  &deploymentsv1.PreflightRequest{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := globalPreviewProblem(recorded, tc.req, &scopedEdge{scope: "cf-other"})
			if tc.wantErr && err == nil {
				t.Fatal("globalPreviewProblem = nil, want the account mismatch")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("globalPreviewProblem = %v, want nil", err)
			}
		})
	}
}

func TestGlobalPreviewProblemRefusesAProjectOnAnotherEdge(t *testing.T) {
	t.Parallel()

	recorded := bootstrap.PreviewDomain{BaseDomain: "previews.ocel.dev", Edge: cloudflare.Kind}

	err := globalPreviewProblem(recorded, &deploymentsv1.PreflightRequest{Slug: "acme", Edge: &deploymentsv1.EdgeSelection{Kind: string(apigateway.Kind)}}, &scopedEdge{})
	if err == nil {
		t.Fatal("globalPreviewProblem = nil, want a deploy that would write routing no one reads refused")
	}
	for _, want := range []string{"*.previews.ocel.dev", "cloudflare", "api-gateway"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}

	if err := globalPreviewProblem(recorded, &deploymentsv1.PreflightRequest{Slug: "acme", Edge: &deploymentsv1.EdgeSelection{Kind: string(cloudflare.Kind)}}, &scopedEdge{}); err != nil {
		t.Fatalf("globalPreviewProblem = %v, want a project on the holding edge admitted", err)
	}

	legacy := bootstrap.PreviewDomain{BaseDomain: "previews.ocel.dev"}
	if err := globalPreviewProblem(legacy, &deploymentsv1.PreflightRequest{Slug: "acme", Edge: &deploymentsv1.EdgeSelection{Kind: string(apigateway.Kind)}}, &scopedEdge{}); err != nil {
		t.Fatalf("globalPreviewProblem = %v, want a record naming no edge to accuse nobody", err)
	}
}

func TestPreviewExpiry(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name      string
		lifecycle deploymentsv1.Environment_Lifecycle
		want      int64
	}{
		{"ephemeral gets now+ttl", deploymentsv1.Environment_LIFECYCLE_EPHEMERAL, now.Add(previewTTL).Unix()},
		{"persistent has no expiry", deploymentsv1.Environment_LIFECYCLE_PERSISTENT, 0},
		{"unspecified (production) has no expiry", deploymentsv1.Environment_LIFECYCLE_UNSPECIFIED, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := previewExpiry(tc.lifecycle, now); got != tc.want {
				t.Errorf("previewExpiry(%v) = %d, want %d", tc.lifecycle, got, tc.want)
			}
		})
	}
}

func TestEnvironmentNameAtIngest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		env     *deploymentsv1.Environment
		want    string
		wantErr bool
	}{
		{"production is named prod", &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PRODUCTION}, deployEnv, false},
		{
			"a preview is named by its pointer",
			&deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW, Lifecycle: deploymentsv1.Environment_LIFECYCLE_EPHEMERAL, Identity: "pr-7"},
			"pr-7",
			false,
		},
		{
			"a preview named prod is refused",
			&deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW, Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT, Identity: deployEnv},
			"",
			true,
		},
		{
			"a preview name the stack grammar cannot carry is refused",
			&deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: "feature_login_ab12"},
			"",
			true,
		},
		{"no environment is refused", nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := deploy.EnvName(tc.env)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("EnvName = %q, want the environment refused before anything is provisioned", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("EnvName: %v", err)
			}
			if got != tc.want {
				t.Errorf("EnvName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreflightResponse(t *testing.T) {
	t.Parallel()

	preview := bootstrap.Deployed{Present: true, Class: bootstrap.ClassPreview}
	production := bootstrap.Deployed{Present: true, Class: bootstrap.ClassProduction}
	absent := bootstrap.Deployed{Present: false}

	cases := []struct {
		name          string
		required      deploymentsv1.Environment_Class
		preview, prod bootstrap.Deployed
		wantClass     deploymentsv1.Environment_Class
		wantPresent   bool
	}{
		{"deploy with both reports production", deploymentsv1.Environment_CLASS_PRODUCTION, preview, production, deploymentsv1.Environment_CLASS_PRODUCTION, true},
		{"preview with both reports preview", deploymentsv1.Environment_CLASS_PREVIEW, preview, production, deploymentsv1.Environment_CLASS_PREVIEW, true},
		{"preview required, preview present", deploymentsv1.Environment_CLASS_PREVIEW, preview, absent, deploymentsv1.Environment_CLASS_PREVIEW, true},
		{"production required, production present", deploymentsv1.Environment_CLASS_PRODUCTION, absent, production, deploymentsv1.Environment_CLASS_PRODUCTION, true},
		{"deploy in a preview-only account reports preview", deploymentsv1.Environment_CLASS_PRODUCTION, preview, absent, deploymentsv1.Environment_CLASS_PREVIEW, true},
		{"preview in a production-only account reports production", deploymentsv1.Environment_CLASS_PREVIEW, absent, production, deploymentsv1.Environment_CLASS_PRODUCTION, true},
		{"empty account reports absent", deploymentsv1.Environment_CLASS_PREVIEW, absent, absent, deploymentsv1.Environment_CLASS_UNSPECIFIED, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := preflightResponse(tc.required, tc.preview, tc.prod)
			if got.GetInfraClass() != tc.wantClass || got.GetInfrastructurePresent() != tc.wantPresent {
				t.Errorf("preflightResponse() = {class=%v present=%v}, want {class=%v present=%v}",
					got.GetInfraClass(), got.GetInfrastructurePresent(), tc.wantClass, tc.wantPresent)
			}
		})
	}
}

func routeOwners(byHostname map[string]string, fail map[string]bool) routeOwnerFunc {
	return func(_ context.Context, hostname string) (string, error) {
		if fail[hostname] {
			return "", errors.New("the edge said no")
		}
		return byHostname[hostname], nil
	}
}

func TestDomainClaims(t *testing.T) {
	t.Parallel()

	t.Run("answers in request order, naming the owning script", func(t *testing.T) {
		t.Parallel()
		owner := routeOwners(map[string]string{
			"*.preview.app.com": "ocel--other--preview--root",
			"shop.com":          "ocel--shop--prod--web",
		}, nil)

		got := domainClaims(context.Background(), owner, "shop", []string{"*.preview.app.com", "free.com", "shop.com"})

		if len(got) != 3 {
			t.Fatalf("claims = %d, want one per requested hostname", len(got))
		}
		if got[0].GetHostname() != "*.preview.app.com" || got[0].GetStatus() != deploymentsv1.DomainClaim_STATUS_CLAIMED || got[0].GetOwner() != "ocel--other--preview--root" {
			t.Errorf("claim[0] = %+v, want another project's worker reported as the owner", got[0])
		}
		if got[1].GetHostname() != "free.com" || got[1].GetStatus() != deploymentsv1.DomainClaim_STATUS_UNCLAIMED || got[1].GetOwner() != "" {
			t.Errorf("claim[1] = %+v, want an unheld hostname reported unclaimed", got[1])
		}
		if got[2].GetStatus() != deploymentsv1.DomainClaim_STATUS_UNCLAIMED || got[2].GetOwner() != "" {
			t.Errorf("claim[2] = %+v, want this project's own hold to read as free to take", got[2])
		}
	})

	t.Run("a hold under this project's retired name is still its own", func(t *testing.T) {
		t.Parallel()
		owner := routeOwners(map[string]string{"shop.com": "ocel-shop--prod-web"}, nil)

		got := domainClaims(context.Background(), owner, "shop", []string{"shop.com"})
		if len(got) != 1 || got[0].GetStatus() != deploymentsv1.DomainClaim_STATUS_UNCLAIMED {
			t.Errorf("claims = %+v, want the pre-cutover hold to read as free to take", got)
		}
	})

	t.Run("an edge that cannot answer leaves the claim unspecified", func(t *testing.T) {
		t.Parallel()
		failing := domainClaims(context.Background(), routeOwners(nil, map[string]bool{"app.com": true}), "shop", []string{"app.com"})
		if len(failing) != 1 || failing[0].GetHostname() != "app.com" || failing[0].GetStatus() != deploymentsv1.DomainClaim_STATUS_UNSPECIFIED {
			t.Errorf("claims = %+v, want a failed lookup reported as unanswerable", failing)
		}
	})

	t.Run("no domains asks the edge nothing", func(t *testing.T) {
		t.Parallel()
		asked := 0
		owner := func(context.Context, string) (string, error) {
			asked++
			return "", nil
		}
		if got := domainClaims(context.Background(), owner, "shop", nil); got != nil {
			t.Errorf("claims = %+v, want none", got)
		}
		if asked != 0 {
			t.Errorf("edge lookups = %d, want 0", asked)
		}
	})
}

func TestKnownSlugs(t *testing.T) {
	t.Parallel()

	present := bootstrap.Deployed{Present: true, StateBucket: "ocel-state"}

	cases := []struct {
		name      string
		substrate bootstrap.Deployed
		slug      string
	}{
		{"no slug sent, so there is nothing to look up", present, ""},
		{"a substrate that is not bootstrapped", bootstrap.Deployed{Present: false}, "my-app"},
		{"a substrate present but holding no state bucket", bootstrap.Deployed{Present: true}, "my-app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := knownSlugs(context.Background(), aws.Config{}, tc.substrate, tc.slug); got != nil {
				t.Errorf("knownSlugs() = %v, want nil", got)
			}
		})
	}
}

func TestToPreviewEnvironments(t *testing.T) {
	t.Parallel()

	t.Run("maps every stack through", func(t *testing.T) {
		t.Parallel()
		stacks := []deploy.PreviewStack{
			{Identity: "feature_login_ab12", Lifecycle: deploymentsv1.Environment_LIFECYCLE_EPHEMERAL, Label: "pr-7", CreatedAt: 100, ExpiresAt: 200},
			{Identity: "staging", Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT},
		}

		got := toPreviewEnvironments(stacks)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].GetIdentity() != "feature_login_ab12" || got[0].GetLifecycle() != deploymentsv1.Environment_LIFECYCLE_EPHEMERAL ||
			got[0].GetLabel() != "pr-7" || got[0].GetCreatedAt() != 100 || got[0].GetExpiresAt() != 200 {
			t.Errorf("first env = %+v, want the ephemeral entry mapped through", got[0])
		}
		if got[1].GetIdentity() != "staging" || got[1].GetLifecycle() != deploymentsv1.Environment_LIFECYCLE_PERSISTENT {
			t.Errorf("second env = %+v, want the persistent entry mapped through", got[1])
		}
	})

	t.Run("no stacks map to no environments", func(t *testing.T) {
		t.Parallel()
		if got := toPreviewEnvironments(nil); len(got) != 0 {
			t.Errorf("toPreviewEnvironments(nil) = %+v, want empty", got)
		}
	})
}

func TestPreflightSubstrates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		required    deploymentsv1.Environment_Class
		present     map[string]bool
		wantAsked   []string
		wantPreview bool
		wantProd    bool
	}{
		{
			"a preview that is there asks about nothing else",
			deploymentsv1.Environment_CLASS_PREVIEW,
			map[string]bool{bootstrap.PreviewStackName: true, bootstrap.StackName: true},
			[]string{bootstrap.PreviewStackName},
			true, false,
		},
		{
			"a production that is there asks about nothing else",
			deploymentsv1.Environment_CLASS_PRODUCTION,
			map[string]bool{bootstrap.PreviewStackName: true, bootstrap.StackName: true},
			[]string{bootstrap.StackName},
			false, true,
		},
		{
			"a missing preview falls back to asking about production",
			deploymentsv1.Environment_CLASS_PREVIEW,
			map[string]bool{bootstrap.StackName: true},
			[]string{bootstrap.PreviewStackName, bootstrap.StackName},
			false, true,
		},
		{
			"a missing production falls back to asking about preview",
			deploymentsv1.Environment_CLASS_PRODUCTION,
			map[string]bool{bootstrap.PreviewStackName: true},
			[]string{bootstrap.StackName, bootstrap.PreviewStackName},
			true, false,
		},
		{
			"an empty account is asked about both",
			deploymentsv1.Environment_CLASS_PREVIEW,
			nil,
			[]string{bootstrap.PreviewStackName, bootstrap.StackName},
			false, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfn := &presenceCFN{present: tc.present}

			preview, production, err := (&Server{}).preflightSubstrates(context.Background(), cfn, "eu-west-1", tc.required)
			if err != nil {
				t.Fatalf("preflightSubstrates: %v", err)
			}
			if asked := cfn.questions(); !slices.Equal(asked, tc.wantAsked) {
				t.Errorf("describes = %v, want %v", asked, tc.wantAsked)
			}
			if preview.Present != tc.wantPreview || production.Present != tc.wantProd {
				t.Errorf("presence = {preview=%t production=%t}, want {preview=%t production=%t}",
					preview.Present, production.Present, tc.wantPreview, tc.wantProd)
			}
		})
	}
}

func TestPreflightSubstratesFallbackMessage(t *testing.T) {
	t.Parallel()

	cfn := &presenceCFN{present: map[string]bool{bootstrap.StackName: true}}
	preview, production, err := (&Server{}).preflightSubstrates(context.Background(), cfn, "eu-west-1", deploymentsv1.Environment_CLASS_PREVIEW)
	if err != nil {
		t.Fatalf("preflightSubstrates: %v", err)
	}

	got := preflightResponse(deploymentsv1.Environment_CLASS_PREVIEW, preview, production)
	if !got.GetInfrastructurePresent() || got.GetInfraClass() != deploymentsv1.Environment_CLASS_PRODUCTION {
		t.Errorf("preflightResponse() = {class=%v present=%v}, want the production infra still reported",
			got.GetInfraClass(), got.GetInfrastructurePresent())
	}
}
