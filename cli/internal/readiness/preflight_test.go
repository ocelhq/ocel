package readiness

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestAFrameworkAppThatFallsBackToAContainerIsAskedAboutAgainWithoutItsFramework(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Serverless: &project.Serverless{Framework: "next", Detected: true}},
	}}
	req := Request{Tier: environmentv1.Tier_TIER_PRODUCTION}
	sent := newPreflightRequest(cfg, req)
	if !reflect.DeepEqual(sent.GetFrameworks(), []string{"next"}) {
		t.Fatalf("first preflight names frameworks %v, want [next]: nothing yet says web cannot run serverless", sent.GetFrameworks())
	}

	t.Run("a provider that runs containers first", func(t *testing.T) {
		t.Parallel()

		resent, differs := resolvedPreflightRequest(cfg, req, "fake", sent, &contractv1.PreflightResponse{Computes: []string{"container", "serverless"}})
		if !differs {
			t.Fatal("the preflight is not asked again, so the bootstrap it reports still requires what next needs for an app that runs a container")
		}
		if len(resent.GetFrameworks()) != 0 {
			t.Errorf("second preflight names frameworks %v, want none", resent.GetFrameworks())
		}
		if named := resent.GetContainers(); len(named) != 1 || named[0].GetApp() != "web" {
			t.Errorf("second preflight names containers %v, want web, so its architecture is read in the same call", named)
		}
	})

	t.Run("a provider that runs serverless first", func(t *testing.T) {
		t.Parallel()

		if _, differs := resolvedPreflightRequest(cfg, req, "fake", sent, &contractv1.PreflightResponse{Computes: []string{"serverless", "container"}}); differs {
			t.Error("the preflight is asked again although web resolves serverless and its framework was already named")
		}
	})

	t.Run("credentials the provider refused", func(t *testing.T) {
		t.Parallel()

		resp := &contractv1.PreflightResponse{
			Computes:           []string{"container"},
			CredentialProblems: []*contractv1.CredentialProblem{{Provider: "fake", Message: "could not authenticate"}},
		}
		if _, differs := resolvedPreflightRequest(cfg, req, "fake", sent, resp); differs {
			t.Error("the preflight is asked again although the credentials it runs with were refused")
		}
	})
}
