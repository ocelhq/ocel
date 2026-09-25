package providerkit_test

import (
	"context"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func TestTheRecordsInlineBindingsKeepAreWrittenByThemAlone(t *testing.T) {
	vars, _ := served(t)
	ctx := context.Background()
	record := func(name string) *bindingsv1.Binding {
		return &bindingsv1.Binding{
			Name:       name,
			Source:     "ocel.json",
			Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Url: "postgres://u:p@db/shop"}},
		}
	}
	inline := naming.InlineRecordName(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "orders")

	for name, req := range map[string]*envvarsv1.SetBindingRequest{
		"a publisher writing a reserved name":       {Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION, Binding: record(inline), Owner: "terraform"},
		"the inline owner writing an ordinary name": {Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION, Binding: record("orders"), Owner: naming.InlineRecordOwner},
	} {
		if _, err := vars.SetBinding(ctx, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: code = %v, want %v", name, connect.CodeOf(err), connect.CodeInvalidArgument)
		}
	}

	if _, err := vars.SetBinding(ctx, &envvarsv1.SetBindingRequest{Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION, Binding: record(inline), Owner: naming.InlineRecordOwner}); err != nil {
		t.Fatalf("the inline owner writing its own record: %v", err)
	}
}
