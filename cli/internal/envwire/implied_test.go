package envwire

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestScopeImpliesTheVariablesATiersInlineBindingsRead(t *testing.T) {
	cfg := &projectconfig.Config{Bindings: []projectconfig.Binding{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "analytics", External: "warehouse"},
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "orders", Tier: environmentv1.Tier_TIER_PRODUCTION, Inline: &projectconfig.Inline{
			Postgres: &projectconfig.PostgresInline{
				Host: projectconfig.Value{Literal: "db"}, Database: projectconfig.Value{Literal: "orders"},
				Username: projectconfig.Value{Variable: "ORDERS_USER"}, Password: "ORDERS_PASSWORD",
			},
		}},
	}}

	want := []envgate.Implied{{Group: "postgres.orders", Site: "bindings.postgres.orders", Keys: []string{"ORDERS_PASSWORD", "ORDERS_USER"}}}
	if got := Scope(cfg, false, "").Implied; !reflect.DeepEqual(got, want) {
		t.Errorf("production Implied = %+v, want %+v", got, want)
	}
	if got := Scope(cfg, true, "pr-12").Implied; len(got) != 0 {
		t.Errorf("preview Implied = %+v, want none: orders binds production alone", got)
	}
	if got := Scope(cfg, true, "pr-12").OtherTiers; !reflect.DeepEqual(got, want) {
		t.Errorf("preview OtherTiers = %+v, want %+v: the app may not declare what production's binding reads", got, want)
	}
	if got := Scope(cfg, false, "").OtherTiers; len(got) != 0 {
		t.Errorf("production OtherTiers = %+v, want none: production takes every binding it reads", got)
	}
	if got := DevScope(cfg).Implied; len(got) != 0 {
		t.Errorf("dev Implied = %+v, want none: ocel dev stands up its own resources", got)
	}
}
