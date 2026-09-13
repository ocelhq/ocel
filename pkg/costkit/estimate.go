package costkit

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/structpb"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

var MonthlyHours = decimal.NewFromInt(730)

const DefaultProfile = costv1.Profile_PROFILE_MODERATE

func Profiles() []costv1.Profile {
	return []costv1.Profile{costv1.Profile_PROFILE_LIGHT, costv1.Profile_PROFILE_MODERATE, costv1.Profile_PROFILE_HEAVY}
}

func ProfileName(profile costv1.Profile) string {
	return strings.ToLower(strings.TrimPrefix(profile.String(), "PROFILE_"))
}

type Band struct{ Light, Moderate, Heavy float64 }

func (b Band) at(profile costv1.Profile) decimal.Decimal {
	switch profile {
	case costv1.Profile_PROFILE_LIGHT:
		return decimal.NewFromFloat(b.Light)
	case costv1.Profile_PROFILE_HEAVY:
		return decimal.NewFromFloat(b.Heavy)
	default:
		return decimal.NewFromFloat(b.Moderate)
	}
}

type Component struct {
	Name       string
	Unit       string
	Rate       string
	Quantity   decimal.Decimal
	UsageBased bool
	Assumption string
	Needs      []string
}

type Pricing func(*Subject)

type Table map[string]Pricing

type Subject struct {
	Resource *costv1.Resource
	Region   string

	profile   costv1.Profile
	overrides map[string]any
	unknown   []string
	usageKeys []string
	fromFile  []string
	read      []string
	faults    []*UsageError
	free      bool
	built     []built
}

type built struct {
	Component
	unknown []string
}

func (s *Subject) Free() { s.free = true }

func (s *Subject) Add(c Component) {
	if c.Assumption == "" && len(s.usageKeys) > 0 {
		if len(s.fromFile) > 0 {
			c.Assumption = strings.Join(s.fromFile, ", ") + " from usage file"
		} else {
			c.Assumption = fmt.Sprintf("%s profile: %s %s", ProfileName(s.profile), c.Quantity, c.Unit)
		}
	}
	for _, path := range c.Needs {
		s.touch(path)
	}
	s.built = append(s.built, built{Component: c, unknown: s.unknown})
	s.usageKeys, s.fromFile, s.unknown = nil, nil, nil
}

func (s *Subject) Usage(key string, band Band) decimal.Decimal {
	s.usageKeys = append(s.usageKeys, key)
	s.read = append(s.read, key)
	raw, ok := walk(s.overrides, key)
	if !ok {
		return band.at(s.profile)
	}
	number, ok := raw.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		s.faults = append(s.faults, &UsageError{
			Resource: s.Resource.GetId(), Key: key,
			Reason: fmt.Sprintf("which is %#v, not a finite non-negative number", raw),
		})
		return band.at(s.profile)
	}
	s.fromFile = append(s.fromFile, key)
	return decimal.NewFromFloat(number)
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

func (s *Subject) Unknown(path string) bool {
	return slices.Contains(s.Resource.GetUnknown(), path)
}

func (s *Subject) touch(path string) bool {
	if !s.Unknown(path) {
		return false
	}
	if !slices.Contains(s.unknown, path) {
		s.unknown = append(s.unknown, path)
	}
	return true
}

func (s *Subject) value(path string) (any, bool) {
	if s.touch(path) {
		return nil, false
	}
	return walk(s.Resource.GetProperties().AsMap(), path)
}

func walk(root map[string]any, path string) (any, bool) {
	var current any = root
	for _, segment := range strings.Split(path, ".") {
		switch held := current.(type) {
		case map[string]any:
			var ok bool
			if current, ok = held[segment]; !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(held) {
				return nil, false
			}
			current = held[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func (s *Subject) Number(path string) decimal.Decimal {
	raw, ok := s.value(path)
	if !ok {
		return decimal.Zero
	}
	switch v := raw.(type) {
	case float64:
		return decimal.NewFromFloat(v)
	case string:
		if parsed, err := decimal.NewFromString(v); err == nil {
			return parsed
		}
	}
	return decimal.Zero
}

func (s *Subject) String(path string) string {
	raw, ok := s.value(path)
	if !ok {
		return ""
	}
	str, _ := raw.(string)
	return str
}

func (s *Subject) Bool(path string) bool {
	raw, ok := s.value(path)
	if !ok {
		return false
	}
	b, _ := raw.(bool)
	return b
}

func (s *Subject) List(path string) []any {
	raw, ok := s.value(path)
	if !ok {
		return nil
	}
	list, _ := raw.([]any)
	return list
}

func (s *Subject) Has(path string) bool {
	_, ok := s.value(path)
	return ok
}

func Estimate(card *Card, table Table, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	set := req.GetResources()
	if set == nil {
		return nil, fmt.Errorf("nothing to price")
	}
	profile := req.GetUsage().GetProfile()
	if profile == costv1.Profile_PROFILE_UNSPECIFIED {
		profile = DefaultProfile
	}
	est := &costv1.Estimate{
		Currency:     card.Currency,
		RatesVersion: card.Version,
		Profile:      profile,
		Coverage:     &costv1.Coverage{UnsupportedTypes: map[string]uint32{}},
	}
	supported := map[string]bool{}
	fixed := map[string]decimal.Decimal{}
	usage := map[string]decimal.Decimal{}
	var totalFixed, totalUsage decimal.Decimal
	account := &ledger{card: card, spent: map[rateKey]decimal.Decimal{}}
	priced := map[string]bool{}
	for _, resource := range set.GetResources() {
		pricing, known := table[resource.GetType()]
		if !known {
			est.Coverage.Unsupported++
			est.Coverage.UnsupportedTypes[resource.GetType()]++
			est.Resources = append(est.Resources, &costv1.ResourceEstimate{
				Resource: resource.GetId(), Status: costv1.ResourceEstimate_STATUS_UNSUPPORTED,
			})
			continue
		}
		supported[resource.GetType()] = true
		subject := &Subject{
			Resource:  resource,
			Region:    resource.GetRegion(),
			profile:   profile,
			overrides: overridesFor(req.GetUsage(), resource.GetId()),
		}
		pricing(subject)
		if err := subject.faulted(); err != nil {
			return nil, err
		}
		priced[resource.GetId()] = true
		held := account.price(subject)
		est.Resources = append(est.Resources, held.estimate)
		switch held.estimate.Status {
		case costv1.ResourceEstimate_STATUS_FREE:
			est.Coverage.Free++
		case costv1.ResourceEstimate_STATUS_NO_PRICE:
			est.Coverage.NoPrice++
		default:
			est.Coverage.Supported++
		}
		fixed[resource.GetScope()] = fixed[resource.GetScope()].Add(held.fixed)
		usage[resource.GetScope()] = usage[resource.GetScope()].Add(held.usage)
		totalFixed = totalFixed.Add(held.fixed)
		totalUsage = totalUsage.Add(held.usage)
	}
	if err := unpriced(req.GetUsage(), priced); err != nil {
		return nil, err
	}
	est.Coverage.SupportedTypes = slices.Sorted(maps.Keys(supported))
	est.MonthlyFixed = money(totalFixed)
	est.MonthlyUsage = money(totalUsage)
	est.Scopes = rollUp(set.GetScopes(), fixed, usage)
	est.Notes = account.notes
	return est, nil
}

func overridesFor(usage *costv1.Usage, id string) map[string]any {
	if held := usage.GetResources()[id]; held != nil {
		return held.AsMap()
	}
	return nil
}

type pricedResource struct {
	estimate     *costv1.ResourceEstimate
	fixed, usage decimal.Decimal
}

type ledger struct {
	card  *Card
	spent map[rateKey]decimal.Decimal
	notes []string
}

const AllowanceAssumed = "a free allowance the account gets once is assumed unspent: a scan cannot see what the account already used this month, so a deploy beside other use of the same allowance costs more than shown"

func (l *ledger) charge(rate Rate, quantity decimal.Decimal) (cost, marginal decimal.Decimal) {
	if rate.Allowance != AllowanceAccount {
		return rate.Cost(quantity)
	}
	l.note(AllowanceAssumed)
	key := rateKey{rate.ID, rate.Region}
	from := l.spent[key]
	l.spent[key] = from.Add(quantity)
	return rate.Between(from, from.Add(quantity))
}

func (l *ledger) note(text string) {
	if text != "" && !slices.Contains(l.notes, text) {
		l.notes = append(l.notes, text)
	}
}

func (l *ledger) price(subject *Subject) pricedResource {
	out := pricedResource{estimate: &costv1.ResourceEstimate{Resource: subject.Resource.GetId()}}
	if subject.free || len(subject.built) == 0 {
		out.estimate.Status = costv1.ResourceEstimate_STATUS_FREE
		return out
	}
	priced := 0
	for _, b := range subject.built {
		component := &costv1.CostComponent{
			Name:             b.Name,
			Unit:             b.Unit,
			UsageBased:       b.UsageBased,
			Assumption:       b.Assumption,
			RateRef:          b.Rate,
			DependsOnUnknown: b.unknown,
		}
		out.estimate.Components = append(out.estimate.Components, component)
		if len(b.unknown) > 0 {
			continue
		}
		component.MonthlyQuantity = b.Quantity.String()
		rate, found, fellBack := l.card.Lookup(b.Rate, subject.Region)
		if !found {
			component.PriceNotFound = true
			continue
		}
		if fellBack {
			l.note(rate.Note)
		}
		cost, marginal := l.charge(rate, b.Quantity)
		cost = cost.Round(moneyPlaces)
		component.UnitPrice = marginal.String()
		component.MonthlyCost = money(cost)
		priced++
		if b.UsageBased {
			out.usage = out.usage.Add(cost)
		} else {
			out.fixed = out.fixed.Add(cost)
		}
	}
	if priced == 0 {
		out.estimate.Status = costv1.ResourceEstimate_STATUS_NO_PRICE
		return out
	}
	out.estimate.Status = costv1.ResourceEstimate_STATUS_PRICED
	out.estimate.MonthlyFixed = money(out.fixed)
	out.estimate.MonthlyUsage = money(out.usage)
	return out
}

func rollUp(scopes []*costv1.Scope, fixed, usage map[string]decimal.Decimal) []*costv1.ScopeTotal {
	parent := make(map[string]string, len(scopes))
	for _, scope := range scopes {
		parent[scope.GetId()] = scope.GetParent()
	}
	totalFixed := map[string]decimal.Decimal{}
	totalUsage := map[string]decimal.Decimal{}
	for id := range fixed {
		for at := id; at != ""; at = parent[at] {
			totalFixed[at] = totalFixed[at].Add(fixed[id])
			totalUsage[at] = totalUsage[at].Add(usage[id])
		}
	}
	totals := make([]*costv1.ScopeTotal, 0, len(scopes))
	for _, scope := range scopes {
		totals = append(totals, &costv1.ScopeTotal{
			Scope:        scope.GetId(),
			MonthlyFixed: money(totalFixed[scope.GetId()]),
			MonthlyUsage: money(totalUsage[scope.GetId()]),
		})
	}
	return totals
}

const moneyPlaces = 2

func money(amount decimal.Decimal) string { return amount.StringFixed(moneyPlaces) }

func Struct(values map[string]any) (*structpb.Struct, error) {
	return structpb.NewStruct(values)
}

func Tables(tables ...Table) Table {
	merged := Table{}
	for _, table := range tables {
		maps.Copy(merged, table)
	}
	return merged
}
