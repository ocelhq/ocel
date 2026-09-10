package naming

import (
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func TestBindableAs(t *testing.T) {
	for _, tc := range []struct {
		declared resourcesv1.ResourceType
		want     bindingsv1.BindingType
		bindable bool
	}{
		{resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, bindingsv1.BindingType_BINDING_TYPE_POSTGRES, true},
		{resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, bindingsv1.BindingType_BINDING_TYPE_BUCKET, true},
		{resourcesv1.ResourceType_RESOURCE_TYPE_CONTAINER, bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED, false},
		{resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED, bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED, false},
	} {
		got, bindable := BindableAs(tc.declared)
		if bindable != tc.bindable {
			t.Errorf("BindableAs(%v) bindable = %v, want %v", tc.declared, bindable, tc.bindable)
		}
		if bindable && got != tc.want {
			t.Errorf("BindableAs(%v) = %v, want %v", tc.declared, got, tc.want)
		}
	}
}

func TestBindableResourceTypesIsTheDomainOfBindableAs(t *testing.T) {
	listed := map[resourcesv1.ResourceType]bool{}
	for _, typ := range BindableResourceTypes() {
		listed[typ] = true
	}
	for _, value := range resourcesv1.ResourceType_value {
		typ := resourcesv1.ResourceType(value)
		_, bindable := BindableAs(typ)
		if bindable != listed[typ] {
			t.Errorf("%v is bindable = %v but listed = %v; a new enum member desynced the two", typ, bindable, listed[typ])
		}
	}
}

func TestEveryBindingTypeButCustomHasADeclarableCounterpart(t *testing.T) {
	reachable := map[bindingsv1.BindingType]bool{}
	for _, typ := range BindableResourceTypes() {
		kind, _ := BindableAs(typ)
		reachable[kind] = true
	}
	for _, value := range bindingsv1.BindingType_value {
		typ := bindingsv1.BindingType(value)
		switch typ {
		case bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED, bindingsv1.BindingType_BINDING_TYPE_CUSTOM:
			if reachable[typ] {
				t.Errorf("%v is reachable from a declared resource, and nothing declares one", typ)
			}
		default:
			if !reachable[typ] {
				t.Errorf("%v can be published but nothing can declare a resource that binds to it", typ)
			}
		}
	}
}
