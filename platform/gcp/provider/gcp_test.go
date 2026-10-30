package gcp

import (
	"reflect"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/platform/gcp/provider/control"
)

func TestCloudRunDeclaresNoFunctionSizeBudget(t *testing.T) {
	if got := pushing(t, "").Facts().MaxFunctionBytes; got != 0 {
		t.Errorf("Facts().MaxFunctionBytes = %d, want none: a Cloud Run function is a container image, which no unzipped-code limit splits", got)
	}
}

func TestTheBindingsTheFactsNameAreTheOnesTheResourceHooksServe(t *testing.T) {
	p := newProvider(&control.Provider{})
	if got, want := p.Facts().Bindings, resources.ServedBindingTypes(p.resourceHooks()); !slices.Equal(got, want) {
		t.Errorf("Facts().Bindings = %v, want %v: the control assembly names the bindings without linking their hooks, and a binding named but not served is refused only at deploy", got, want)
	}
}

func TestEveryHookTheFullProviderSetsTheControlAssemblySetsToo(t *testing.T) {
	full, assembly := reflect.ValueOf(newProvider(&control.Provider{}).Hooks()), reflect.ValueOf((&control.Provider{}).Hooks())
	for i := range full.NumField() {
		hook := full.Type().Field(i).Name
		if full.Field(i).IsNil() != assembly.Field(i).IsNil() {
			t.Errorf("Hooks().%s is set on one of the full provider and the control assembly and not the other: nil means unsupported, so the two disagree on what this vendor supports", hook)
			continue
		}
		if full.Field(i).Kind() != reflect.Pointer || full.Field(i).IsNil() {
			continue
		}
		group, refusing := full.Field(i).Elem(), assembly.Field(i).Elem()
		for j := range group.NumField() {
			if group.Field(j).IsNil() != refusing.Field(j).IsNil() {
				t.Errorf("Hooks().%s.%s is set on one of the full provider and the control assembly and not the other", hook, group.Type().Field(j).Name)
			}
		}
	}
}
