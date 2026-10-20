package container_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
)

func TestAResourceContainerIsNamedByProjectEnvAppResourceKindAndItsHash(t *testing.T) {
	release, err := naming.ParseReleaseToken("r1a2b3c4d")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ref  provider.StackRef
		want string
	}{
		{provider.StackRef{Project: "acme", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}, "acme-prod-main-pg-93fe95"},
		{provider.StackRef{Project: "acme", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", release)}, "acme-prod-web-main-pg-a41a36"},
	} {
		got, err := container.Name(tc.ref, "main", "pg")
		if err != nil {
			t.Errorf("%s: %v", tc.ref.Name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Name(%s) = %q, want %q", tc.ref.Name, got, tc.want)
		}
	}
}

func TestAResourceContainerThatCannotFitIsRefusedNamingTheResource(t *testing.T) {
	resource := strings.Repeat("ledger-", 7) + "x"
	ref := provider.StackRef{Project: "acme", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}
	got, err := container.Name(ref, resource, "pg")
	if err == nil {
		t.Fatalf("Name = %q, want a refusal", got)
	}
	if refused := (refusal.Refusal{}); !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("Name failed with %v, want a refusal coded %q", err, refusal.CodeInvalid)
	}
	if !strings.Contains(err.Error(), resource) {
		t.Errorf("the refusal reads %q and never names %s", err, resource)
	}
}
