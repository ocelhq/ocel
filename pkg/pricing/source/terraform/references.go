package terraform

import (
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/pricing/source/internal/properties"
)

type referencesOf func(resources []resource, props []properties.Properties) []map[string]string

var nonResourceRoots = []string{"count", "data", "each", "local", "module", "path", "self", "terraform", "var"}

const (
	referencesKey    = "references"
	constantValueKey = "constant_value"
	idProperty       = "id"
	arnProperty      = "arn"
)

func (c configuration) references(resources []resource, props []properties.Properties) []map[string]string {
	configured := c.expressions()
	out := referencesByValue(resources, props)
	for i, r := range resources {
		expressions := configured[configAddress(moduleCalls(r.module), r.typ+"."+r.name)]
		for path, refs := range expressionReferences(expressions, "") {
			var targets []string
			for _, ref := range refs {
				if target, found := resolve(ref, r.module, resources); found && !slices.Contains(targets, target) {
					targets = append(targets, target)
				}
			}
			if len(targets) == 1 {
				out[i][path] = targets[0]
			}
		}
	}
	return out
}

func expressionReferences(expressions map[string]any, path string) map[string][]string {
	out := map[string][]string{}
	for key, value := range expressions {
		at := properties.Join(path, key)
		switch held := value.(type) {
		case map[string]any:
			if !isExpression(held) {
				for nested, refs := range expressionReferences(held, at) {
					out[nested] = refs
				}
				continue
			}
			if refs := stringList(held[referencesKey]); len(refs) > 0 {
				out[at] = refs
			}
		case []any:
			for i, block := range held {
				object, ok := block.(map[string]any)
				if !ok {
					continue
				}
				blockPath := at
				if len(held) > 1 {
					blockPath = properties.Join(at, strconv.Itoa(i))
				}
				for nested, refs := range expressionReferences(object, blockPath) {
					out[nested] = refs
				}
			}
		}
	}
	return out
}

func isExpression(object map[string]any) bool {
	_, references := object[referencesKey]
	_, constant := object[constantValueKey]
	return references || constant || len(object) == 0
}

func stringList(value any) []string {
	list, _ := value.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func resolve(ref, module string, resources []resource) (string, bool) {
	parts := addressParts(ref)
	if len(parts) < 2 || slices.Contains(nonResourceRoots, parts[0]) {
		return "", false
	}
	name, _, indexed := strings.Cut(parts[1], "[")
	local := parts[0] + "." + parts[1]
	var matches []string
	for _, candidate := range resources {
		if candidate.module != module || candidate.typ != parts[0] || candidate.name != name {
			continue
		}
		if !indexed || localAddress(candidate) == local {
			matches = append(matches, candidate.address)
		}
	}
	if len(matches) != 1 {
		return "", false
	}
	return matches[0], true
}

func referencesByValue(resources []resource, props []properties.Properties) []map[string]string {
	byARN := map[string][]resource{}
	byID := map[string][]resource{}
	for i, r := range resources {
		if arn, ok := props[i].Values[arnProperty].(string); ok && arn != "" {
			byARN[arn] = append(byARN[arn], r)
		}
		if id, ok := props[i].Values[idProperty].(string); ok && id != "" {
			byID[id] = append(byID[id], r)
		}
	}
	out := make([]map[string]string, len(resources))
	for i, r := range resources {
		out[i] = map[string]string{}
		for path, text := range stringLeaves(props[i].Values, "") {
			if path == idProperty || path == arnProperty {
				continue
			}
			owners := byARN[text]
			if len(owners) == 0 {
				owners = slices.DeleteFunc(slices.Clone(byID[text]), func(owner resource) bool { return !namesType(path, owner.typ) })
			}
			if len(owners) == 1 && owners[0].address != r.address {
				out[i][path] = owners[0].address
			}
		}
	}
	return out
}

var identifierSuffixes = []string{"_identifier", "_id", "_arn", "_name"}

func namesType(path, typ string) bool {
	stem := path[strings.LastIndex(path, ".")+1:]
	for _, suffix := range identifierSuffixes {
		stem = strings.TrimSuffix(stem, suffix)
	}
	return strings.HasSuffix(typ, "_"+stem) || lastWord(stem) == lastWord(typ)
}

func lastWord(name string) string {
	return name[strings.LastIndex(name, "_")+1:]
}

func stringLeaves(values map[string]any, path string) map[string]string {
	out := map[string]string{}
	for key, value := range values {
		at := properties.Join(path, key)
		switch held := value.(type) {
		case string:
			out[at] = held
		case map[string]any:
			if properties.IsMapValued(key) {
				continue
			}
			for nested, text := range stringLeaves(held, at) {
				out[nested] = text
			}
		}
	}
	return out
}
