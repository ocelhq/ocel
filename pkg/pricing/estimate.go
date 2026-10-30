package pricing

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/shopspring/decimal"

	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
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
	Price      Query
	Allowance  Allowance
	Quantity   decimal.Decimal
	UsageBased bool
	Assumption string
	Needs      []string
}

const AllowanceAssumed = "a free allowance the account gets once is assumed unspent: a scan cannot see what the account already used this month, so a deploy beside other use of the same allowance costs more than shown"

const (
	currency        = "USD"
	candidatesShown = 10
)

type Input struct {
	Set   *costv1.ResourceSet
	Usage *costv1.Usage
}

func Estimate(ctx context.Context, catalog Catalog, table Table, set *costv1.ResourceSet, usage *costv1.Usage) (*costv1.Estimate, error) {
	estimates, err := EstimateAll(ctx, catalog, table, []Input{{Set: set, Usage: usage}})
	if err != nil {
		return nil, err
	}
	return estimates[0], nil
}

func EstimateAll(ctx context.Context, catalog Catalog, table Table, inputs []Input) ([]*costv1.Estimate, error) {
	formulated := make([]*formulatedInput, 0, len(inputs))
	var subjects []*Subject
	for _, input := range inputs {
		f, err := formulate(table, input)
		if err != nil {
			return nil, err
		}
		formulated = append(formulated, f)
		subjects = append(subjects, f.subjects...)
	}
	answers, err := lookup(ctx, catalog, subjects)
	if err != nil {
		return nil, err
	}
	estimates := make([]*costv1.Estimate, 0, len(formulated))
	for _, f := range formulated {
		est, err := f.estimate(answers)
		if err != nil {
			return nil, err
		}
		estimates = append(estimates, est)
	}
	return estimates, nil
}

type formulatedInput struct {
	set      *costv1.ResourceSet
	profile  costv1.Profile
	subjects []*Subject
}

func formulate(table Table, input Input) (*formulatedInput, error) {
	if input.Set == nil {
		return nil, fmt.Errorf("nothing to price")
	}
	profile := input.Usage.GetProfile()
	if profile == costv1.Profile_PROFILE_UNSPECIFIED {
		profile = DefaultProfile
	}
	if _, defined := costv1.Profile_name[int32(profile)]; !defined {
		return nil, fmt.Errorf("no usage profile is numbered %d", profile)
	}
	resources := make(map[string]*costv1.Resource, len(input.Set.GetResources()))
	for _, resource := range input.Set.GetResources() {
		resources[resource.GetId()] = resource
	}
	f := &formulatedInput{set: input.Set, profile: profile, subjects: make([]*Subject, len(input.Set.GetResources()))}
	priced := map[string]bool{}
	for i, resource := range input.Set.GetResources() {
		formula, known := table[resource.GetType()]
		if !known {
			continue
		}
		subject := &Subject{Resource: resource, resources: resources, profile: profile, overrides: overridesFor(input.Usage, resource.GetId())}
		formula(subject)
		if err := subject.faulted(); err != nil {
			return nil, err
		}
		priced[resource.GetId()] = true
		f.subjects[i] = subject
	}
	if err := unpriced(input.Usage, priced); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *formulatedInput) estimate(answers map[string]Answer) (*costv1.Estimate, error) {
	est := &costv1.Estimate{
		Currency: currency,
		Profile:  f.profile,
		Coverage: &costv1.Coverage{UnsupportedTypes: map[string]uint32{}},
	}
	account := &tally{answers: answers, spent: map[string]decimal.Decimal{}, catalogs: map[catalogVersion]bool{}}
	supported := map[string]bool{}
	fixed := map[string]decimal.Decimal{}
	usageCost := map[string]decimal.Decimal{}
	var totalFixed, totalUsage decimal.Decimal
	var vendorNotes []string
	for i, resource := range f.set.GetResources() {
		subject := f.subjects[i]
		if subject == nil {
			est.Coverage.Unsupported++
			est.Coverage.UnsupportedTypes[resource.GetType()]++
			est.Resources = append(est.Resources, &costv1.ResourceEstimate{
				Resource: resource.GetId(), Status: costv1.ResourceEstimate_STATUS_UNSUPPORTED,
			})
			continue
		}
		supported[resource.GetType()] = true
		for _, text := range subject.notes {
			if !slices.Contains(vendorNotes, text) {
				vendorNotes = append(vendorNotes, text)
			}
		}
		out := account.price(subject)
		est.Resources = append(est.Resources, out.estimate)
		switch out.estimate.Status {
		case costv1.ResourceEstimate_STATUS_FREE:
			est.Coverage.Free++
		case costv1.ResourceEstimate_STATUS_NO_PRICE:
			est.Coverage.NoPrice++
		default:
			est.Coverage.Supported++
		}
		fixed[resource.GetScope()] = fixed[resource.GetScope()].Add(out.fixed)
		usageCost[resource.GetScope()] = usageCost[resource.GetScope()].Add(out.usage)
		totalFixed = totalFixed.Add(out.fixed)
		totalUsage = totalUsage.Add(out.usage)
	}
	scopes, err := rollUp(f.set.GetScopes(), fixed, usageCost)
	if err != nil {
		return nil, err
	}
	est.Coverage.SupportedTypes = slices.Sorted(maps.Keys(supported))
	est.MonthlyFixed = money(totalFixed)
	est.MonthlyUsage = money(totalUsage)
	est.Scopes = scopes
	est.Notes = slices.Concat(account.notes, vendorNotes)
	est.Catalogs = account.versions()
	return est, nil
}

func lookup(ctx context.Context, catalog Catalog, subjects []*Subject) (map[string]Answer, error) {
	var queries []Query
	var keys []string
	sent := map[string]bool{}
	for _, subject := range subjects {
		if subject == nil {
			continue
		}
		for _, b := range subject.built {
			if len(b.unknown) > 0 || len(b.absent) > 0 || sent[b.key] {
				continue
			}
			sent[b.key] = true
			queries = append(queries, b.Price)
			keys = append(keys, b.key)
		}
	}
	if len(queries) == 0 {
		return nil, nil
	}
	answers, err := catalog.Lookup(ctx, queries)
	if err != nil {
		return nil, fmt.Errorf("price lookup: %w", err)
	}
	if len(answers) != len(queries) {
		return nil, fmt.Errorf("price lookup: the catalog answered %d of %d queries", len(answers), len(queries))
	}
	byKey := make(map[string]Answer, len(queries))
	for i, key := range keys {
		byKey[key] = answers[i]
	}
	return byKey, nil
}

func overridesFor(usage *costv1.Usage, id string) map[string]any {
	if overrides := usage.GetResources()[id]; overrides != nil {
		return overrides.AsMap()
	}
	return nil
}

type pricedResource struct {
	estimate     *costv1.ResourceEstimate
	fixed, usage decimal.Decimal
}

type catalogVersion struct{ vendor, service, version string }

type tally struct {
	answers  map[string]Answer
	spent    map[string]decimal.Decimal
	catalogs map[catalogVersion]bool
	notes    []string
}

func (l *tally) charge(b built, price Price) (cost, marginal decimal.Decimal) {
	if b.Allowance == AllowanceResource || (b.Allowance == "" && !price.allows()) {
		return price.Cost(b.Quantity)
	}
	if price.allows() && !slices.Contains(l.notes, AllowanceAssumed) {
		l.notes = append(l.notes, AllowanceAssumed)
	}
	from := l.spent[b.key]
	l.spent[b.key] = from.Add(b.Quantity)
	return price.Between(from, from.Add(b.Quantity))
}

func (l *tally) versions() []*costv1.CatalogVersion {
	keys := slices.SortedFunc(maps.Keys(l.catalogs), func(a, b catalogVersion) int {
		return cmp.Or(cmp.Compare(a.vendor, b.vendor), cmp.Compare(a.service, b.service), cmp.Compare(a.version, b.version))
	})
	versions := make([]*costv1.CatalogVersion, 0, len(keys))
	for _, key := range keys {
		versions = append(versions, &costv1.CatalogVersion{Vendor: key.vendor, Service: key.service, Version: key.version})
	}
	return versions
}

func ambiguity(answer Answer) string {
	shown := answer.Candidates[:min(len(answer.Candidates), candidatesShown)]
	return fmt.Sprintf("matches %d skus: %s", max(answer.Matches, len(answer.Candidates)), strings.Join(shown, ", "))
}

func (l *tally) price(subject *Subject) pricedResource {
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
			DependsOnUnknown: b.unknown,
		}
		out.estimate.Components = append(out.estimate.Components, component)
		if len(b.absent) > 0 {
			component.PriceStatus = costv1.CostComponent_PRICE_STATUS_MISSING
			component.PriceDetail = strings.Join(b.absent, ", ")
			continue
		}
		if len(b.unknown) > 0 {
			component.PriceStatus = costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT
			continue
		}
		component.MonthlyQuantity = b.Quantity.String()
		answer := l.answers[b.key]
		switch {
		case answer.Kind == AnswerPriced && len(answer.Price.Steps) == 0:
			component.PriceStatus = costv1.CostComponent_PRICE_STATUS_MISSING
			component.PriceDetail = fmt.Sprintf("the catalog answered sku %s with no price", answer.Price.SKU)
			continue
		case answer.Kind == AnswerPriced:
		case answer.Kind == AnswerAmbiguous:
			component.PriceStatus = costv1.CostComponent_PRICE_STATUS_AMBIGUOUS
			component.PriceDetail = ambiguity(answer)
			continue
		case answer.Kind == AnswerUnavailable:
			component.PriceStatus = costv1.CostComponent_PRICE_STATUS_UNAVAILABLE
			component.PriceDetail = answer.Reason
			continue
		default:
			component.PriceStatus = costv1.CostComponent_PRICE_STATUS_MISSING
			component.PriceDetail = answer.Reason
			continue
		}
		cost, marginal := l.charge(b, answer.Price)
		cost = cost.Round(moneyPlaces)
		l.catalogs[catalogVersion{b.Price.Vendor, b.Price.Service, answer.Price.CatalogVersion}] = true
		component.PriceStatus = costv1.CostComponent_PRICE_STATUS_PRICED
		component.Sku = answer.Price.SKU
		component.PriceSource = answer.Price.Source
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

func Unpriced(c *costv1.CostComponent) bool {
	return c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED
}

func rollUp(scopes []*costv1.Scope, fixed, usage map[string]decimal.Decimal) ([]*costv1.ScopeTotal, error) {
	parent := make(map[string]string, len(scopes))
	for _, scope := range scopes {
		parent[scope.GetId()] = scope.GetParent()
	}
	totalFixed := map[string]decimal.Decimal{}
	totalUsage := map[string]decimal.Decimal{}
	for id := range fixed {
		visited := map[string]bool{}
		for at := id; at != ""; at = parent[at] {
			if visited[at] {
				return nil, fmt.Errorf("scope %s is its own ancestor", at)
			}
			visited[at] = true
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
	return totals, nil
}

const moneyPlaces = 2

func money(amount decimal.Decimal) string { return amount.StringFixed(moneyPlaces) }

func Combine(rules ...Rules) Table {
	combined := Table{}
	for _, vendor := range rules {
		for typ, formula := range vendor.Table {
			combined[typ] = func(s *Subject) {
				s.notes = append(s.notes, vendor.Notes...)
				formula(s)
			}
		}
	}
	return combined
}
