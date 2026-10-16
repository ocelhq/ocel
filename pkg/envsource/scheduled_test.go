package envsource_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
)

func TestTheProjectsASyncServesAreThoseRegisteringAnEnvSourceReadOnASchedule(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	ctx := context.Background()
	options, err := json.Marshal(envsource.ExecOptions{Command: []string{"op", "inject"}, Format: envsource.FormatJSON})
	if err != nil {
		t.Fatal(err)
	}
	exec, err := envsource.NewDescriptor("exec", options)
	if err != nil {
		t.Fatal(err)
	}
	for tier, registrations := range map[environment.Tier][]envsource.Registration{
		environment.TierProduction: {
			infisicalRegistration("shop", "https://infisical.example.com", universal, ""),
			{Project: "admin", Descriptor: exec, Folders: []string{""}},
			infisicalRegistration("blog", "https://infisical.example.com", cloudIdentity, ""),
		},
		environment.TierPreview: {infisicalRegistration("docs", "https://infisical.example.com", universal, "")},
	} {
		for _, registration := range registrations {
			if _, err := envsource.Register(ctx, store, tier, registration); err != nil {
				t.Fatal(err)
			}
		}
	}

	got, err := envsource.ScheduledProjects(ctx, store.KeyValues, environment.TierProduction)

	if err != nil || !slices.Equal(got, []string{"blog", "shop"}) {
		t.Errorf("ScheduledProjects() = %v, %v, want blog and shop, whose sources are read on a schedule", got, err)
	}
}
