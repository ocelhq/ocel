package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

type Formula func(*Subject)

type Table map[string]Formula

type Subject struct {
	Resource *costv1.Resource

	owner     *Subject
	prefix    string
	reference string
	dangling  bool

	resources map[string]*costv1.Resource
	profile   costv1.Profile
	overrides map[string]any
	unknown   []string
	missing   map[string]string
	computed  []string
	usageKeys []string
	fromFile  []string
	read      []string
	faults    []*UsageError
	free      bool
	built     []built
	notes     []string
}

type built struct {
	Component
	key     string
	unknown []string
	absent  []string
}

const regionPath = "region"

func (s *Subject) root() *Subject {
	if s.owner != nil {
		return s.owner
	}
	return s
}

func (s *Subject) Free() { s.root().free = true }

func (s *Subject) Region() string {
	s.touch(regionPath)
	return s.Resource.GetRegion()
}

func (s *Subject) Add(c Component) {
	r := s.root()
	if c.Assumption == "" && len(r.usageKeys) > 0 {
		if len(r.fromFile) > 0 {
			c.Assumption = strings.Join(r.fromFile, ", ") + " from usage file"
		} else {
			c.Assumption = fmt.Sprintf("%s profile: %s %s", ProfileName(r.profile), c.Quantity, c.Unit)
		}
	}
	for _, path := range c.Needs {
		if _, absent := r.missing[path]; !r.touch(path) && absent {
			r.pend(path)
		}
	}
	b := built{Component: c, key: c.Price.Key()}
	for _, path := range r.unknown {
		if reason, absent := r.missing[path]; absent {
			b.absent = append(b.absent, reason)
		} else {
			b.unknown = append(b.unknown, path)
		}
	}
	r.built = append(r.built, b)
	r.usageKeys, r.fromFile, r.unknown = nil, nil, nil
}

func (s *Subject) When(conditions ...func() bool) bool {
	r := s.root()
	before := r.unknown
	var undecided []string
	held := true
	for _, condition := range conditions {
		r.unknown = nil
		if !condition() && len(r.unknown) == 0 {
			held = false
		}
		undecided = append(undecided, r.unknown...)
	}
	r.unknown = before
	if !held {
		r.usageKeys, r.fromFile = nil, nil
		return false
	}
	for _, path := range undecided {
		r.pend(path)
	}
	return true
}

func (s *Subject) Usage(key string, band Band) decimal.Decimal {
	r := s.root()
	r.usageKeys = append(r.usageKeys, key)
	r.read = append(r.read, key)
	raw, ok := walk(r.overrides, key)
	if !ok {
		return band.at(r.profile)
	}
	number, ok := raw.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		r.faults = append(r.faults, &UsageError{
			Resource: r.Resource.GetId(), Key: key,
			Reason: fmt.Sprintf("which is %#v, not a finite non-negative number", raw),
		})
		return band.at(r.profile)
	}
	r.fromFile = append(r.fromFile, key)
	return decimal.NewFromFloat(number)
}

func (s *Subject) Reference(path string) (*Subject, bool) {
	r := s.root()
	name := s.prefix + path
	if s.dangling {
		r.pend(s.reference)
		return &Subject{Resource: &costv1.Resource{}, owner: r, reference: s.reference, dangling: true}, false
	}
	id, referenced := s.Resource.GetReferences()[path]
	target := r.resources[id]
	if target == nil {
		if _, state := s.value(path); referenced || state == readPresent {
			r.absent(name, name+" names no resource in this scan")
		} else if state == readAbsent {
			r.absent(name, name+" is not set")
		}
		return &Subject{Resource: &costv1.Resource{}, owner: r, reference: name, dangling: true}, false
	}
	return &Subject{Resource: target, owner: r, prefix: name + "."}, true
}

func (s *Subject) Unknown(path string) bool {
	if s.dangling {
		return true
	}
	return slices.ContainsFunc(s.Resource.GetUnknown(), func(unknown string) bool {
		return related(segments(unknown), segments(path))
	})
}

func segments(path string) []string { return strings.Split(path, ".") }

func related(a, b []string) bool { return within(a, b) || within(b, a) }

func within(ancestor, path []string) bool {
	switch {
	case len(ancestor) == 0:
		return true
	case len(path) > 0 && ancestor[0] == path[0] && within(ancestor[1:], path[1:]):
		return true
	case ancestor[0] == "0" && within(ancestor[1:], path):
		return true
	default:
		return len(path) > 0 && path[0] == "0" && within(ancestor, path[1:])
	}
}

func (s *Subject) touch(path string) bool {
	if s.dangling {
		s.root().pend(s.reference)
		return true
	}
	if !s.Unknown(path) {
		return false
	}
	s.root().pend(s.prefix + path)
	return true
}

func (s *Subject) pend(path string) {
	if !slices.Contains(s.unknown, path) {
		s.unknown = append(s.unknown, path)
	}
}

func (s *Subject) MarkUnset(path, consequence string) {
	if !s.touch(path) {
		name := s.prefix + path
		s.root().absent(name, name+" is not set, so "+consequence)
	}
}

func (s *Subject) DeclareComputed(path string) {
	r := s.root()
	r.computed = append(r.computed, s.prefix+path)
}

func (s *Subject) require(path string, raw any) {
	name := s.prefix + path
	if raw == nil && slices.Contains(s.root().computed, name) {
		s.root().absent(name, name+" is not set, so the provider chooses it on apply")
		return
	}
	if raw == nil {
		s.root().absent(name, name+" is not set")
		return
	}
	shown, _ := json.Marshal(raw)
	s.root().absent(name, fmt.Sprintf("%s is %s, which the rule cannot read", name, shown))
}

func (s *Subject) absent(path, reason string) {
	s.pend(path)
	if s.missing == nil {
		s.missing = map[string]string{}
	}
	if _, seen := s.missing[path]; !seen {
		s.missing[path] = reason
	}
}

type reading int

const (
	readUnknown reading = iota
	readAbsent
	readPresent
)

func (s *Subject) value(path string) (any, reading) {
	if s.touch(path) {
		return nil, readUnknown
	}
	raw, ok := walk(s.Resource.GetProperties().AsMap(), path)
	if !ok || raw == nil {
		return nil, readAbsent
	}
	return raw, readPresent
}

func walk(root map[string]any, path string) (any, bool) {
	var current any = root
	for _, segment := range segments(path) {
		switch typed := current.(type) {
		case map[string]any:
			next, ok := typed[segment]
			if !ok && segment != "0" {
				return nil, false
			}
			if ok {
				current = next
			}
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil && len(typed) == 1 {
				block, ok := typed[0].(map[string]any)
				next, found := block[segment]
				if !ok || !found {
					return nil, false
				}
				current = next
				continue
			}
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func number(raw any) (decimal.Decimal, bool) {
	switch v := raw.(type) {
	case float64:
		return decimal.NewFromFloat(v), true
	case string:
		parsed, err := decimal.NewFromString(v)
		return parsed, err == nil
	}
	return decimal.Zero, false
}

func (s *Subject) Number(path string) decimal.Decimal {
	raw, state := s.value(path)
	if state == readUnknown {
		return decimal.Zero
	}
	parsed, ok := number(raw)
	if state == readAbsent || !ok {
		s.require(path, raw)
		return decimal.Zero
	}
	return parsed
}

func (s *Subject) NumberOr(path string, fallback decimal.Decimal) decimal.Decimal {
	if _, state := s.value(path); state == readAbsent {
		return fallback
	}
	return s.Number(path)
}

func (s *Subject) String(path string) string {
	raw, state := s.value(path)
	if state == readUnknown {
		return ""
	}
	str, ok := raw.(string)
	if state == readAbsent || !ok {
		s.require(path, raw)
		return ""
	}
	return str
}

func (s *Subject) StringOr(path, fallback string) string {
	if _, state := s.value(path); state == readAbsent {
		return fallback
	}
	return s.String(path)
}

func (s *Subject) Quantity(path string, parse func(string) (decimal.Decimal, bool)) decimal.Decimal {
	raw, state := s.value(path)
	if state == readUnknown {
		return decimal.Zero
	}
	var text string
	switch v := raw.(type) {
	case string:
		text = v
	case float64:
		text = strconv.FormatFloat(v, 'f', -1, 64)
	}
	parsed, ok := parse(text)
	if state == readAbsent || !ok {
		s.require(path, raw)
		return decimal.Zero
	}
	return parsed
}

func (s *Subject) QuantityOr(path string, parse func(string) (decimal.Decimal, bool), fallback decimal.Decimal) decimal.Decimal {
	if _, state := s.value(path); state == readAbsent {
		return fallback
	}
	return s.Quantity(path, parse)
}

func (s *Subject) Bool(path string) bool {
	raw, state := s.value(path)
	if state == readUnknown {
		return false
	}
	b, ok := raw.(bool)
	if state == readAbsent || !ok {
		s.require(path, raw)
		return false
	}
	return b
}

func (s *Subject) BoolOr(path string, fallback bool) bool {
	if _, state := s.value(path); state == readAbsent {
		return fallback
	}
	return s.Bool(path)
}

func (s *Subject) List(path string) []any {
	raw, state := s.value(path)
	switch typed := raw.(type) {
	case []any:
		return typed
	case map[string]any:
		return []any{typed}
	}
	if state != readUnknown {
		s.require(path, raw)
	}
	return nil
}

func (s *Subject) Has(path string) bool {
	_, state := s.value(path)
	return state == readPresent
}

type UsageError struct {
	Resource string
	Key      string
	Reason   string
}

func (e *UsageError) Error() string {
	if e.Key == "" {
		return fmt.Sprintf("the usage names %s, %s", e.Resource, e.Reason)
	}
	return fmt.Sprintf("the usage for %s sets %s, %s", e.Resource, e.Key, e.Reason)
}

func unpriced(usage *costv1.Usage, priced map[string]bool) error {
	var faults []error
	for _, id := range slices.Sorted(maps.Keys(usage.GetResources())) {
		if !priced[id] {
			faults = append(faults, &UsageError{Resource: id, Reason: "which is no resource this scan prices"})
		}
	}
	return errors.Join(faults...)
}

func (s *Subject) faulted() error {
	faults := make([]error, 0, len(s.faults))
	for _, fault := range s.faults {
		faults = append(faults, fault)
	}
	for _, key := range leaves(s.overrides, "") {
		if !slices.Contains(s.read, key) {
			faults = append(faults, &UsageError{
				Resource: s.Resource.GetId(), Key: key,
				Reason: fmt.Sprintf("which pricing a %s never reads", s.Resource.GetType()),
			})
		}
	}
	return errors.Join(faults...)
}

func leaves(root map[string]any, prefix string) []string {
	var out []string
	for key, value := range root {
		path := prefix + key
		if nested, ok := value.(map[string]any); ok && len(nested) > 0 {
			out = append(out, leaves(nested, path+".")...)
			continue
		}
		out = append(out, path)
	}
	slices.Sort(out)
	return out
}
