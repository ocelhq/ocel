package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestRequirePreviewClass(t *testing.T) {
	t.Parallel()

	if err := requirePreviewClass(deploymentsv1.Environment_CLASS_PREVIEW); err != nil {
		t.Fatalf("requirePreviewClass: %v", err)
	}
	for _, class := range []deploymentsv1.Environment_Class{
		deploymentsv1.Environment_CLASS_PRODUCTION,
		deploymentsv1.Environment_CLASS_UNSPECIFIED,
	} {
		err := requirePreviewClass(class)
		if err == nil {
			t.Fatalf("expected %v to be refused", class)
		}
		if !strings.Contains(err.Error(), "--preview") {
			t.Errorf("error must name the flag, got %q", err)
		}
	}
}

func TestPreviewBaseDomainArg(t *testing.T) {
	t.Parallel()

	t.Run("normalizes the domain", func(t *testing.T) {
		t.Parallel()
		got, err := previewBaseDomainArg("  Preview.Acme.com. ")
		if err != nil {
			t.Fatalf("previewBaseDomainArg: %v", err)
		}
		if got != "preview.acme.com" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("refuses a wildcard and names the domain to pass", func(t *testing.T) {
		t.Parallel()
		_, err := previewBaseDomainArg("*.preview.acme.com")
		if err == nil {
			t.Fatal("expected a wildcard to be refused")
		}
		if !strings.Contains(err.Error(), "preview.acme.com") {
			t.Errorf("error must name the domain to pass instead, got %q", err)
		}
	})

	t.Run("refuses what is not a domain", func(t *testing.T) {
		t.Parallel()
		for _, in := range []string{"", "acme", "https://preview.acme.com"} {
			if _, err := previewBaseDomainArg(in); err == nil {
				t.Errorf("expected %q to be refused", in)
			}
		}
	})
}

func TestGlobalPreviewDomain(t *testing.T) {
	t.Parallel()

	recorded := bootstrap.PreviewDomain{BaseDomain: "preview.acme.com", CloudflareAccount: "acct", GrammarMin: 1, GrammarMax: 1}

	t.Run("is nil when nothing is recorded", func(t *testing.T) {
		t.Parallel()
		owner := func(context.Context, string) (string, error) { return "", nil }
		if got := globalPreviewDomain(context.Background(), owner, bootstrap.PreviewDomain{}); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("reports the route the shared entry actually holds", func(t *testing.T) {
		t.Parallel()
		var asked string
		owner := func(_ context.Context, hostname string) (string, error) {
			asked = hostname
			return edge.PreviewEntryOwner, nil
		}
		got := globalPreviewDomain(context.Background(), owner, recorded)
		if asked != "*.preview.acme.com" {
			t.Errorf("asked %q, want the wildcard hostname", asked)
		}
		if !got.GetRouteInstalled() {
			t.Error("RouteInstalled = false, want true")
		}
		if got.GetBaseDomain() != "preview.acme.com" || got.GetCloudflareAccount() != "acct" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("a foreign or missing owner is not installed", func(t *testing.T) {
		t.Parallel()
		for _, owner := range []routeOwnerFunc{
			func(context.Context, string) (string, error) { return "", nil },
			func(context.Context, string) (string, error) { return "someone-elses-worker", nil },
			func(context.Context, string) (string, error) { return "", errors.New("no zone") },
		} {
			if globalPreviewDomain(context.Background(), owner, recorded).GetRouteInstalled() {
				t.Error("RouteInstalled = true, want false")
			}
		}
	})
}

type stateSSM struct {
	params map[string]string
}

func (s *stateSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	raw, ok := s.params[aws.ToString(in.Name)]
	if !ok {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(raw)}}, nil
}

func (s *stateSSM) PutParameter(_ context.Context, in *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	s.params[aws.ToString(in.Name)] = aws.ToString(in.Value)
	return &ssm.PutParameterOutput{}, nil
}

func (s *stateSSM) DeleteParameter(_ context.Context, in *ssm.DeleteParameterInput, _ ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error) {
	delete(s.params, aws.ToString(in.Name))
	return &ssm.DeleteParameterOutput{}, nil
}

func TestGlobalPreviewProjects(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ssmc := &stateSSM{params: map[string]string{}}
	for slug, state := range map[string]edge.StackState{
		"ambient":     {edge.StackKeySlug: "ambient", edge.StackKeyGlobalPreview: "preview.acme.com"},
		"own-domain":  {edge.StackKeySlug: "own-domain"},
		"other-usage": {edge.StackKeySlug: "other-usage", edge.StackKeyGlobalPreview: "preview.old.com"},
	} {
		if err := bootstrap.WriteStackStateFor(ctx, ssmc, bootstrap.ClassPreview, slug, state); err != nil {
			t.Fatalf("WriteStackStateFor(%s): %v", slug, err)
		}
	}

	served, err := globalPreviewProjects(ctx, ssmc, []string{"ambient", "own-domain", "other-usage", "never-deployed"}, "preview.acme.com")
	if err != nil {
		t.Fatalf("globalPreviewProjects: %v", err)
	}
	if len(served) != 1 || served[0] != "ambient" {
		t.Errorf("served = %v, want [ambient]: a project on its own preview domain is not served by the substrate's", served)
	}
}

func TestGlobalPreviewAccountMismatch(t *testing.T) {
	recorded := bootstrap.PreviewDomain{BaseDomain: "preview.acme.com", CloudflareAccount: "recorded-acct"}

	t.Run("a match passes", func(t *testing.T) {
		t.Setenv(cloudflareAccountEnvVar, "recorded-acct")
		if err := globalPreviewAccountMismatch(recorded); err != nil {
			t.Fatalf("globalPreviewAccountMismatch: %v", err)
		}
	})

	t.Run("a mismatch refuses and names both accounts", func(t *testing.T) {
		t.Setenv(cloudflareAccountEnvVar, "ambient-acct")
		err := globalPreviewAccountMismatch(recorded)
		if err == nil {
			t.Fatal("expected a mismatched account to be refused")
		}
		for _, want := range []string{"recorded-acct", "ambient-acct", "preview.acme.com", "ocel domain release"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must name %q, got %q", want, err)
			}
		}
	})

	t.Run("nothing recorded is nothing to compare", func(t *testing.T) {
		t.Setenv(cloudflareAccountEnvVar, "ambient-acct")
		if err := globalPreviewAccountMismatch(bootstrap.PreviewDomain{}); err != nil {
			t.Fatalf("globalPreviewAccountMismatch: %v", err)
		}
	})
}
