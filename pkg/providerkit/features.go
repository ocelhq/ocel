package providerkit

import (
	"fmt"
	"slices"
	"strings"
)

func FeatureLevels(catalogue []Feature, names []string) ([][]string, error) {
	pending := map[string]bool{}
	for _, name := range names {
		pending[name] = true
	}
	var levels [][]string
	placed := map[string]bool{}
	for len(pending) > 0 {
		var level []string
		for _, f := range catalogue {
			if !pending[f.Name] {
				continue
			}
			ready := true
			for _, dep := range f.DependsOn {
				if pending[dep] && !placed[dep] {
					ready = false
				}
			}
			if ready {
				level = append(level, f.Name)
			}
		}
		if len(level) == 0 {
			return nil, fmt.Errorf("no order stands %s up: each waits on another in the set",
				strings.Join(inCatalogueOrder(catalogue, keys(pending)), ", "))
		}
		for _, name := range level {
			delete(pending, name)
			placed[name] = true
		}
		levels = append(levels, level)
	}
	return levels, nil
}

func RequiredFeatures(catalogue []Feature, frameworks []string, edgeKind string) ([]string, error) {
	var needed []string
	for _, f := range catalogue {
		if featureNeeded(f, frameworks, edgeKind) {
			needed = append(needed, f.Name)
		}
	}
	return featureClosure(catalogue, needed)
}

func featureNeeded(f Feature, frameworks []string, edgeKind string) bool {
	for _, need := range f.Needs {
		if id, ok := strings.CutPrefix(need, NeedsFrameworkPrefix); ok && slices.Contains(frameworks, id) {
			return true
		}
		if kind, ok := strings.CutPrefix(need, NeedsEdgePrefix); ok && kind == edgeKind && edgeKind != "" {
			return true
		}
	}
	return false
}

func featureClosure(catalogue []Feature, names []string) ([]string, error) {
	wanted := map[string]bool{}
	var pull func(name string, from string) error
	pull = func(name, from string) error {
		f, ok := featureNamed(catalogue, name)
		if !ok {
			return unknownFeature(catalogue, name, from)
		}
		if wanted[name] {
			return nil
		}
		wanted[name] = true
		for _, dep := range f.DependsOn {
			if err := pull(dep, name); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range names {
		if err := pull(name, ""); err != nil {
			return nil, err
		}
	}
	return inCatalogueOrder(catalogue, keys(wanted)), nil
}

func featureDeleteOrder(catalogue []Feature, names []string) ([]string, error) {
	levels, err := FeatureLevels(catalogue, names)
	if err != nil {
		return nil, err
	}
	var out []string
	for i := len(levels) - 1; i >= 0; i-- {
		out = append(out, levels[i]...)
	}
	return out, nil
}

func featureDrop(catalogue []Feature, standing, requested []string) []string {
	doomed := map[string]bool{}
	for _, name := range standing {
		if !slices.Contains(requested, name) {
			doomed[name] = true
		}
	}
	for grew := true; grew; {
		grew = false
		for _, f := range catalogue {
			if doomed[f.Name] || !slices.Contains(standing, f.Name) {
				continue
			}
			for _, dep := range f.DependsOn {
				if doomed[dep] {
					doomed[f.Name], grew = true, true
				}
			}
		}
	}
	return inCatalogueOrder(catalogue, keys(doomed))
}

func missingFeatures(standing, required []string) []string {
	var out []string
	for _, name := range required {
		if !slices.Contains(standing, name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func featureNamed(catalogue []Feature, name string) (Feature, bool) {
	for _, f := range catalogue {
		if f.Name == name {
			return f, true
		}
	}
	return Feature{}, false
}

func featureNames(catalogue []Feature) []string {
	out := make([]string, 0, len(catalogue))
	for _, f := range catalogue {
		out = append(out, f.Name)
	}
	return out
}

func inCatalogueOrder(catalogue []Feature, chosen []string) []string {
	var out []string
	for _, f := range catalogue {
		if slices.Contains(chosen, f.Name) {
			out = append(out, f.Name)
		}
	}
	return out
}

func unknownFeature(catalogue []Feature, name, from string) error {
	offered := strings.Join(featureNames(catalogue), ", ")
	if offered == "" {
		offered = "no bootstrap features at all"
	}
	if from != "" {
		return Refuse(CodeInvalid, "%s depends on %q, which this provider does not offer; it offers %s", from, name, offered)
	}
	return Refuse(CodeInvalid, "this provider has no bootstrap feature named %q; it offers %s", name, offered)
}

func keys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name, on := range set {
		if on {
			out = append(out, name)
		}
	}
	return out
}
