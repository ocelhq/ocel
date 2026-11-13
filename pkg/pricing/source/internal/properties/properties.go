package properties

import "slices"

const (
	RegionPath   = "region"
	locationPath = "location"
	tagsPath     = "tags"
)

var mapValued = []string{
	"annotations",
	"effective_annotations",
	"effective_labels",
	"labels",
	"limits",
	tagsPath,
	"tags_all",
	"terraform_labels",
	"variables",
}

func IsMapValued(name string) bool {
	return slices.Contains(mapValued, name)
}

type Properties struct {
	Values  map[string]any
	Unknown []string
}

func New(values map[string]any, unknown []string) Properties {
	if values == nil {
		values = map[string]any{}
	}
	slices.Sort(unknown)
	return Properties{Values: values, Unknown: slices.Compact(unknown)}
}

func (p Properties) IsUnknown(path string) bool {
	return slices.Contains(p.Unknown, path)
}

func (p Properties) Tags() map[string]string {
	held, _ := p.Values[tagsPath].(map[string]any)
	if len(held) == 0 {
		return nil
	}
	out := make(map[string]string, len(held))
	for key, value := range held {
		if text, ok := value.(string); ok {
			out[key] = text
		}
	}
	return out
}

func (p Properties) OwnRegion(vendor string) (region string, known, decided bool) {
	if region, ok := p.Values[RegionPath].(string); ok && region != "" {
		return region, true, true
	}
	if p.IsUnknown(RegionPath) {
		return "", false, true
	}
	if vendor != VendorGCP {
		return "", false, false
	}
	if location, ok := p.Values[locationPath].(string); ok && location != "" {
		region, known := LocationRegion(location)
		return region, known, true
	}
	if p.IsUnknown(locationPath) {
		return "", false, true
	}
	return "", false, false
}

func OnlyObject(list []any) (map[string]any, bool) {
	if len(list) != 1 {
		return nil, false
	}
	object, ok := list[0].(map[string]any)
	return object, ok
}

func Join(path, segment string) string {
	if path == "" {
		return segment
	}
	return path + "." + segment
}
