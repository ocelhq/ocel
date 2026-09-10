package naming

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

var bindable = map[resourcesv1.ResourceType]bindingsv1.BindingType{
	resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES: bindingsv1.BindingType_BINDING_TYPE_POSTGRES,
	resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET:   bindingsv1.BindingType_BINDING_TYPE_BUCKET,
}

func BindableAs(t resourcesv1.ResourceType) (bindingsv1.BindingType, bool) {
	kind, ok := bindable[t]
	return kind, ok
}

func BindableResourceTypes() []resourcesv1.ResourceType {
	return slices.SortedFunc(maps.Keys(bindable), func(a, b resourcesv1.ResourceType) int {
		return cmp.Compare(a, b)
	})
}

const resourceTypePrefix = "RESOURCE_TYPE_"

func ResourceTypeNamed(name string) (resourcesv1.ResourceType, bool) {
	value, ok := resourcesv1.ResourceType_value[resourceTypePrefix+strings.ToUpper(name)]
	return resourcesv1.ResourceType(value), ok
}

func ResourceTypeName(t resourcesv1.ResourceType) string {
	return strings.ToLower(strings.TrimPrefix(t.String(), resourceTypePrefix))
}
