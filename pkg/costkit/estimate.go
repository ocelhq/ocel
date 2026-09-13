package costkit

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/structpb"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

var MonthlyHours = decimal.NewFromInt(730)

const (
	ProfileLight    = "light"
	ProfileModerate = "moderate"
	ProfileHeavy    = "heavy"
)

type Band struct{ Light, Moderate, Heavy float64 }

func (b Band) at(profile string) decimal.Decimal {
	switch profile {
	case ProfileLight:
		return decimal.NewFromFloat(b.Light)
	case ProfileHeavy:
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
}

type Pricing func(*Subject)

type Table map[string]Pricing

type Subject struct {
	Resource *costv1.Resource
	Region   string

	profile   string
	overrides map[string]any
	unknown   []string
	usageKey  string
	usageFrom string
	free      bool
	built     []built
}

type built struct {
	Component
	unknown []string
}

func (s *Subject) Free() { s.free = true }

func (s *Subject) Add(c Component) {
	if c.Assumption == "" && s.usageKey != "" {
		if s.usageFrom == "file" {
			c.Assumption = s.usageKey + " from usage file"
		} else {
			c.Assumption = fmt.Sprintf("%s profile: %s %s", s.profile, c.Quantity, c.Unit)
		}
	}
	s.built = append(s.built, built{Component: c, unknown: s.unknown})
	s.unknown, s.usageKey, s.usageFrom = nil, "", ""
}

func (s *Subject) Usage(key string, band Band) decimal.Decimal {
	s.usageKey = key
	if raw, ok := s.overrides[key]; ok {
		if number, ok := raw.(float64); ok {
			s.usageFrom = "file"
			return decimal.NewFromFloat(number)
		}
	}
	s.usageFrom = "profile"
	return band.at(s.profile)
}

func (s *Subject) Unknown(path string) bool {
	return slices.Contains(s.Resource.GetUnknown(), path)
}

func (s *Subject) value(path string) (any, bool) {
	if s.Unknown(path) {
		s.unknown = append(s.unknown, path)
		return nil, false
	}
	var current any = s.Resource.GetProperties().AsMap()
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		if current, ok = object[segment]; !ok {
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
	if profile == "" {
		profile = ProfileModerate
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
		priced := price(card, subject)
		est.Resources = append(est.Resources, priced.estimate)
		switch priced.estimate.Status {
		case costv1.ResourceEstimate_STATUS_FREE:
			est.Coverage.Free++
		case costv1.ResourceEstimate_STATUS_NO_PRICE:
			est.Coverage.NoPrice++
		default:
			est.Coverage.Supported++
		}
		fixed[resource.GetScope()] = fixed[resource.GetScope()].Add(priced.fixed)
		usage[resource.GetScope()] = usage[resource.GetScope()].Add(priced.usage)
		totalFixed = totalFixed.Add(priced.fixed)
		totalUsage = totalUsage.Add(priced.usage)
	}
	est.Coverage.SupportedTypes = slices.Sorted(maps.Keys(supported))
	est.MonthlyFixed = money(totalFixed)
	est.MonthlyUsage = money(totalUsage)
	est.Scopes = rollUp(set.GetScopes(), fixed, usage)
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

func price(card *Card, subject *Subject) pricedResource {
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
		rate, found := card.Lookup(b.Rate, subject.Region)
		if !found {
			component.PriceNotFound = true
			continue
		}
		cost, marginal := rate.Cost(b.Quantity)
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

func money(amount decimal.Decimal) string { return amount.StringFixed(2) }

func Struct(values map[string]any) *structpb.Struct {
	s, err := structpb.NewStruct(values)
	if err != nil {
		panic(err)
	}
	return s
}
