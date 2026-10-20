package providerserver

import (
	"context"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestASessionManagesItsResourcesUnderTheNamespaceItsProcessServes(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "acme")
	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) { return stubProvider{}, nil }}}
	if err := s.configure(context.Background(), provider.Settings{ProjectDir: t.TempDir()}); err != nil {
		t.Fatalf("configure() = %v", err)
	}
	if got := s.managedBy(); got != "acme" {
		t.Errorf("managedBy() = %q, want acme, the namespace %s names", got, provider.NamespaceEnvVar)
	}
}

func TestASessionRefusesANamespaceItsProcessCannotServe(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "Not A Namespace")
	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) { return stubProvider{}, nil }}}
	if err := s.configure(context.Background(), provider.Settings{ProjectDir: t.TempDir()}); err == nil {
		t.Fatal("configure() = nil, want a refusal: the tags it writes would name no namespace")
	}
}

func TestAnAppsAndItsInfrasTagsNameTheNamespaceThatManagesThem(t *testing.T) {
	t.Parallel()

	spec, err := buildDeploySpec(productionRequest(&contractv1.ManifestApp{Name: "web", BuildId: buildID}), "p1")
	if err != nil {
		t.Fatalf("buildDeploySpec() error = %v", err)
	}
	if got := appTags("acme", spec, spec.Apps[0])["ocel:managed-by"]; got != "acme" {
		t.Errorf("app ocel:managed-by = %q, want acme: the deploy credential of namespace acme reaches only what acme tags", got)
	}
	if got := infraTags("acme", spec)["ocel:managed-by"]; got != "acme" {
		t.Errorf("infra ocel:managed-by = %q, want acme", got)
	}
}
