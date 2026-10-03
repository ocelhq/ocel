package gcp_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

var secretID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,255}$`)

var accountID = regexp.MustCompile(`^[a-z]([-a-z0-9]{4,28}[a-z0-9])$`)

func longNames(t *testing.T) gcp.Names {
	t.Helper()
	return names(t, newProvider(t, gcp.Options{Project: "acme-production-workloads", Region: "europe-west1"}))
}

func TestAGatewayServiceFitsCloudRunWhateverItsProjectAndEnvironmentAreCalled(t *testing.T) {
	names := longNames(t)

	for _, scope := range [][2]string{
		{"shop", "prod"},
		{"a-project-slug-that-runs-on-and-on", "feature-branch-with-a-very-long-name"},
	} {
		service := names.RealtimeGateway(scope[0], scope[1])
		if len(service) > 49 || !cloudRunName.MatchString(service) {
			t.Errorf("RealtimeGateway(%q, %q) = %q (%d characters), which Cloud Run will not take as a service name of at most 49", scope[0], scope[1], service, len(service))
		}
		if !strings.HasPrefix(service, names.Namespace().String()+"-") {
			t.Errorf("RealtimeGateway(%q, %q) = %q, want it under the namespace", scope[0], scope[1], service)
		}
	}
}

func TestEachEnvironmentHasAGatewayAndKeysOfItsOwn(t *testing.T) {
	names := longNames(t)

	if names.RealtimeGateway("shop", "prod") == names.RealtimeGateway("shop", "pr-7") {
		t.Error("two environments share one gateway, and a preview's keys would be trusted in production")
	}
	if names.RealtimeGateway("shop-prod", "x") == names.RealtimeGateway("shop", "prod-x") {
		t.Error("two scopes whose dashes fall differently share one gateway")
	}
	if names.RealtimeKeysSecret("shop", "prod") == names.RealtimeKeysSecret("shop", "pr-7") {
		t.Error("two environments share one keys secret")
	}
	app, err := names.Service("shop", "prod", "realtime", "realtime")
	if err != nil {
		t.Fatal(err)
	}
	if names.RealtimeGateway("shop", "prod") == app {
		t.Error("the gateway is named as an app called realtime would be, and one would replace the other")
	}
}

func TestEveryRealtimeSecretIsOneSecretManagerTakesAndEachResourceHasItsOwn(t *testing.T) {
	names := longNames(t)
	project, env := "a-project-slug-that-runs-on-and-on", "feature-branch-with-a-very-long-name"

	keys := names.RealtimeKeysSecret(project, env)
	app, chat := names.RealtimeSigningSecret(project, env, "app"), names.RealtimeSigningSecret(project, env, "chat")
	for _, secret := range []string{keys, app, chat} {
		if !secretID.MatchString(secret) {
			t.Errorf("%q is no secret id Secret Manager takes", secret)
		}
	}
	if app == chat || app == keys {
		t.Errorf("signing secrets %q and %q and keys secret %q are not apart", app, chat, keys)
	}
}

func TestEachTierRunsItsGatewaysAsAnAccountOfItsOwn(t *testing.T) {
	names := longNames(t)

	production, preview := names.RealtimeAccount(environment.TierProduction), names.RealtimeAccount(environment.TierPreview)
	for _, account := range []string{production, preview} {
		if !accountID.MatchString(account) {
			t.Errorf("RealtimeAccount() = %q, which IAM will not take as an account id", account)
		}
	}
	if production == preview || production == names.WorkloadAccount(environment.TierProduction) {
		t.Errorf("the production gateway runs as %q, which is not an account of its own", production)
	}
}
