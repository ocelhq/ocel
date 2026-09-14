package pulumi

import "strings"

func providerRegions(records []record) map[string]string {
	regions := map[string]string{}
	for _, held := range records {
		if !strings.HasPrefix(held.Type, providerTypeHead) {
			continue
		}
		if region, _ := held.Inputs["region"].(string); region != "" {
			regions[held.URN] = region
			continue
		}
		if zone, _ := held.Inputs["zone"].(string); zone != "" {
			regions[held.URN] = zoneRegion(zone)
		}
	}
	return regions
}

func zoneRegion(zone string) string {
	if at := strings.LastIndex(zone, "-"); at > 0 {
		return zone[:at]
	}
	return zone
}

func providerURN(ref string) string {
	if at := strings.LastIndex(ref, "::"); at > 0 {
		return ref[:at]
	}
	return ref
}

func configRegion(config map[string]any, pkg string) string {
	switch held := config[pkg+":region"].(type) {
	case string:
		return held
	case map[string]any:
		value, _ := held["value"].(string)
		return value
	}
	return ""
}
