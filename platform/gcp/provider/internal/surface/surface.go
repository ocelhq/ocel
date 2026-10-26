package surface

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type Surface[D, U any] struct {
	Name   string
	Fields []Field[D, U]
	Labels *Labels[D, U]
}

type Field[D, U any] struct {
	Name   string
	Paths  []Path[D]
	Same   func(current, desired *D) bool
	Update func(update *U, current, desired *D)
	Reset  func(desired *D)
	Lowers func(current, desired *D) bool
	Show   func(d *D) string
}

type Labels[D, U any] struct {
	Of     func(d *D) *map[string]string
	Update func(update *U, set map[string]string, removed []string)
}

type Path[D any] struct {
	name   string
	leaves []string
	set    func(desired *D, value []byte) error
}

func NewPath[D, V any](name string, set func(desired *D, value V) error) Path[D] {
	return Path[D]{
		name:   name,
		leaves: leavesOf(name, reflect.TypeFor[V]()),
		set: func(desired *D, raw []byte) error {
			var value V
			strict := json.NewDecoder(bytes.NewReader(raw))
			strict.DisallowUnknownFields()
			if err := strict.Decode(&value); err != nil {
				return err
			}
			return set(desired, value)
		},
	}
}

var timeType = reflect.TypeFor[time.Time]()

func leavesOf(prefix string, t reflect.Type) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t.Kind() == reflect.Struct && t != timeType:
		var leaves []string
		for field := range t.Fields() {
			if !field.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			leaves = append(leaves, leavesOf(prefix+"."+name, field.Type)...)
		}
		return leaves
	case t.Kind() == reflect.Slice || t.Kind() == reflect.Array:
		elem := t.Elem()
		for elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		if elem.Kind() == reflect.Struct && elem != timeType {
			return leavesOf(prefix, elem)
		}
	}
	return []string{prefix}
}

func (s Surface[D, U]) Paths() []string {
	var paths []string
	for _, field := range s.Fields {
		for _, path := range field.Paths {
			paths = append(paths, path.leaves...)
		}
	}
	slices.Sort(paths)
	return paths
}

type Patched[D, U any] struct {
	Desired D

	resource string
	surface  Surface[D, U]
	set      []string
	diffed   []int
	labels   map[string]string
	recorded Recorded
}

func (s Surface[D, U]) Patch(resource string, desired D, patch map[string]any, tags map[string]string, recorded Recorded) (Patched[D, U], error) {
	if err := s.refuseUnpatchable(resource, patch); err != nil {
		return Patched[D, U]{}, err
	}
	patched := Patched[D, U]{resource: resource, surface: s, recorded: recorded}
	for i, field := range s.Fields {
		reached, reset := false, false
		for _, path := range field.Paths {
			value, present := at(patch, path.name)
			if present {
				raw, err := json.Marshal(value)
				if err == nil {
					err = path.set(&desired, raw)
				}
				if err != nil {
					return Patched[D, U]{}, refusal.Refuse(refusal.CodeInvalid,
						"a transform sets %s's %s.%s to a value Google Cloud cannot take: %v",
						resource, s.Name, path.name, err)
				}
				patched.set = append(patched.set, path.name)
				reached = true
				continue
			}
			if slices.Contains(recorded.Paths, path.name) {
				reset = true
			}
		}
		if reset && !reached && field.Reset != nil {
			field.Reset(&desired)
		}
		if reached || reset {
			patched.diffed = append(patched.diffed, i)
		}
	}
	slices.Sort(patched.set)
	if s.Labels != nil {
		if err := RefuseInvalidLabels(resource, tags); err != nil {
			return Patched[D, U]{}, err
		}
		labels := s.Labels.Of(&desired)
		merged := maps.Clone(*labels)
		if merged == nil && len(tags) > 0 {
			merged = map[string]string{}
		}
		maps.Copy(merged, tags)
		if err := refuseTooManyLabels(resource, merged); err != nil {
			return Patched[D, U]{}, err
		}
		*labels = merged
		patched.labels = maps.Clone(tags)
	}
	patched.Desired = desired
	return patched, nil
}

func (s Surface[D, U]) refuseUnpatchable(resource string, patch map[string]any) error {
	return refuseUnpatchable(resource, s.Name, s.Paths(), patch)
}

func refuseUnpatchable(resource, surface string, allowed []string, patch map[string]any) error {
	var refused []string
	walkLeaves("", patch, func(path string, empty bool) {
		reached := slices.ContainsFunc(allowed, func(leaf string) bool {
			return leaf == path || empty && strings.HasPrefix(leaf, path+".")
		})
		if !reached && !slices.Contains(refused, path) {
			refused = append(refused, path)
		}
	})
	if len(refused) == 0 {
		return nil
	}
	slices.Sort(refused)
	named := make([]string, len(refused))
	for i, path := range refused {
		named[i] = surface + "." + path
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform sets %s's %s, which this provider does not patch: drop it (%s takes %s)",
		resource, strings.Join(named, ", "), surface, strings.Join(allowed, ", "))
}

func walkLeaves(prefix string, value any, visit func(path string, empty bool)) {
	join := func(key string) string {
		if prefix == "" {
			return key
		}
		return prefix + "." + key
	}
	switch typed := value.(type) {
	case nil:
		if prefix != "" {
			visit(prefix, true)
		}
	case map[string]any:
		if transform.IsOutput(typed) {
			visit(prefix, false)
			return
		}
		if len(typed) == 0 && prefix != "" {
			visit(prefix, true)
			return
		}
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			walkLeaves(join(key), typed[key], visit)
		}
	case []any:
		if !slices.ContainsFunc(typed, func(item any) bool { _, nested := item.(map[string]any); return nested && !transform.IsOutput(item) }) {
			if len(typed) > 0 || prefix != "" {
				visit(prefix, len(typed) == 0)
			}
			return
		}
		for _, item := range typed {
			walkLeaves(prefix, item, visit)
		}
	default:
		visit(prefix, false)
	}
}

func at(patch map[string]any, path string) (any, bool) {
	var current any = patch
	for _, key := range strings.Split(path, ".") {
		object, nested := current.(map[string]any)
		if !nested {
			return nil, false
		}
		value, present := object[key]
		if !present || value == nil {
			return nil, false
		}
		current = value
	}
	return current, true
}

func (p Patched[D, U]) Sets(path string) bool { return slices.Contains(p.set, path) }

func (p Patched[D, U]) IsEmpty() bool {
	return len(p.set) == 0 && len(p.labels) == 0 && p.recorded.IsEmpty()
}

func (p Patched[D, U]) Recorded() Recorded {
	return Recorded{Paths: slices.Clone(p.set), Labels: slices.Sorted(maps.Keys(p.labels))}
}

func (p Patched[D, U]) Drift(current *D, update *U) ([]string, error) {
	var differences []string
	for _, i := range p.diffed {
		field := p.surface.Fields[i]
		if field.Same == nil || field.Same(current, &p.Desired) {
			continue
		}
		if field.Lowers != nil && field.Lowers(current, &p.Desired) {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"a transform would take %s's %s from %s back to %s, and Google Cloud only lets it rise: patch it back to at least %s",
				p.resource, field.Name, show(field, current), show(field, &p.Desired), show(field, current))
		}
		if field.Update != nil {
			field.Update(update, current, &p.Desired)
		}
		if field.Show != nil {
			differences = append(differences, fmt.Sprintf("%s changes from %s to %s", field.Name, field.Show(current), field.Show(&p.Desired)))
		} else {
			differences = append(differences, field.Name+" changes")
		}
	}
	if p.surface.Labels != nil {
		if changed := p.labelDrift(current, update); changed != "" {
			differences = append(differences, changed)
		}
	}
	return differences, nil
}

func show[D, U any](field Field[D, U], d *D) string {
	if field.Show == nil {
		return "its current value"
	}
	return field.Show(d)
}

func ShowOnOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func (p Patched[D, U]) labelDrift(current *D, update *U) string {
	held := *p.surface.Labels.Of(current)
	set := map[string]string{}
	for key, value := range p.labels {
		if existing, present := held[key]; !present || existing != value {
			set[key] = value
		}
	}
	var removed []string
	for _, key := range p.recorded.Labels {
		if _, kept := p.labels[key]; kept {
			continue
		}
		if _, present := held[key]; present {
			removed = append(removed, key)
		}
	}
	if len(set) == 0 && len(removed) == 0 {
		return ""
	}
	slices.Sort(removed)
	p.surface.Labels.Update(update, set, removed)
	var parts []string
	if len(set) > 0 {
		parts = append(parts, "labels "+strings.Join(slices.Sorted(maps.Keys(set)), ", ")+" are set")
	}
	if len(removed) > 0 {
		parts = append(parts, "labels "+strings.Join(removed, ", ")+" are removed")
	}
	return strings.Join(parts, "; ")
}
