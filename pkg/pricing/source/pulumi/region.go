package pulumi

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/pricing/source/internal/properties"
)

func (d *document) region(held *resourceState, vendor string, props properties.Properties) (string, bool) {
	if region, known, decided := props.OwnRegion(vendor); decided {
		return region, known
	}
	if provider, found := d.providers[providerURN(held.Provider)]; found {
		return providerRegion(provider)
	}
	pkg, _, _ := strings.Cut(held.Type, ":")
	if region, ok := d.config[pkg+":"+properties.RegionPath].(string); ok && region != "" && region != maskedSecret {
		return region, true
	}
	return "", false
}

func providerRegion(provider properties.Properties) (string, bool) {
	if region, ok := provider.Values[properties.RegionPath].(string); ok && region != "" {
		return region, true
	}
	if provider.IsUnknown(properties.RegionPath) {
		return "", false
	}
	if at, ok := provider.Values[zoneProperty].(string); ok && at != "" {
		return properties.ZoneRegion(at), true
	}
	return "", false
}

func providerURN(ref string) string {
	if at := strings.LastIndex(ref, urnSeparator); at > 0 {
		return ref[:at]
	}
	return ref
}
