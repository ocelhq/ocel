package variablescope_test

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/envsource"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

const (
	production = environmentv1.Tier_TIER_PRODUCTION
	preview    = environmentv1.Tier_TIER_PREVIEW
)

func TestAScopeNamesTheVariablesATiersInlineBindingsRead(t *testing.T) {
	cfg := &project.Project{Bindings: []project.Binding{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "analytics", External: "warehouse"},
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "orders", Tier: environmentv1.Tier_TIER_PRODUCTION, Inline: &project.Inline{
			Postgres: &project.PostgresInline{
				Host: project.Value{Literal: "db"}, Database: project.Value{Literal: "orders"},
				Username: project.Value{Variable: "ORDERS_USER"}, Password: "ORDERS_PASSWORD",
			},
		}},
	}}

	want := []variables.BindingVariables{{Group: "postgres.orders", Site: "bindings.postgres.orders", Keys: []string{"ORDERS_PASSWORD", "ORDERS_USER"}}}
	if got := variablescope.Of(cfg, production, "").Bindings; !reflect.DeepEqual(got, want) {
		t.Errorf("production Bindings = %+v, want %+v", got, want)
	}
	if got := variablescope.Of(cfg, preview, "pr-12").Bindings; len(got) != 0 {
		t.Errorf("preview Bindings = %+v, want none: orders binds production alone", got)
	}
	if got := variablescope.Of(cfg, preview, "pr-12").OtherTiers; !reflect.DeepEqual(got, want) {
		t.Errorf("preview OtherTiers = %+v, want %+v: the app may not declare what production's binding reads", got, want)
	}
	if got := variablescope.Of(cfg, production, "").OtherTiers; len(got) != 0 {
		t.Errorf("production OtherTiers = %+v, want none: production takes every binding it reads", got)
	}
	if got := variablescope.ForDev(cfg).Bindings; len(got) != 0 {
		t.Errorf("dev Bindings = %+v, want none: ocel dev provisions its own resources", got)
	}
}

func infisicalConfig() *project.Project {
	tiers := envsource.DefaultTiers()
	tiers.Production = envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{
		Project: "p-1", Environment: "prod",
		Auth: envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVariable: "INFISICAL_CLIENT_ID", ClientSecretVariable: "INFISICAL_CLIENT_SECRET"},
	}}
	return &project.Project{EnvSource: tiers}
}

func TestAScopeNamesTheEnvSourceATierReadsAndTheCredentialsItLogsInWith(t *testing.T) {
	cfg := infisicalConfig()

	want := variables.EnvSource{ID: "infisical:p-1/prod", Credentials: []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"}}
	if got := variablescope.Of(cfg, production, "").EnvSource; !reflect.DeepEqual(got, want) {
		t.Errorf("production EnvSource = %+v, want %+v", got, want)
	}
	if got := variablescope.Of(cfg, preview, "pr-12").EnvSource; !reflect.DeepEqual(got, variables.EnvSource{ID: "builtin"}) {
		t.Errorf("preview EnvSource = %+v, want builtin with no credentials", got)
	}
	if got := variablescope.ForDev(cfg).EnvSource; !reflect.DeepEqual(got, variables.EnvSource{}) {
		t.Errorf("dev EnvSource = %+v, want none: ocel dev reads on this machine", got)
	}
}
