package terraform

import "github.com/ocelhq/ocel/pkg/pricing/source/internal/properties"

type regionOf func(resource, properties.Properties) (string, bool)

func stateRegion(r resource, props properties.Properties) (string, bool) {
	region, known, _ := props.OwnRegion(properties.TypeVendor(r.typ))
	return region, known
}
