package component

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAComponentNamingAKindTheHooksHaveNoFieldForIsReportedUnserved(t *testing.T) {
	t.Parallel()
	registry := Registry{
		{Types: []provider.BindingType{provider.BindingPostgres}},
		{Types: []provider.BindingType{provider.BindingType("queue")}},
	}
	if got := registry.Unserved(); !slices.Equal(got, []provider.BindingType{"queue"}) {
		t.Errorf("Unserved() = %v, want the queue kind no hooks field serves", got)
	}
}
