package conformance

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/costkit"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
)

const costImageDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func runCost(t *testing.T, suite Suite) {
	t.Helper()
	if suite.Server.New == nil {
		t.Skip("the suite carries no Spec, so there is no mux to serve")
	}
	server := httptest.NewServer(providerserver.ConformanceMux(suite.Server))
	t.Cleanup(server.Close)

	providerClient := client(server.Client(), server.URL)
	if _, err := providerClient.Configure(context.Background(), configureWith(t, suite.Options)); err != nil {
		t.Fatalf("Configure() error = %v, want the session configured", err)
	}
	vendor := ""
	if suite.New != nil {
		if held, err := suite.New(context.Background(), provider.Settings{Options: suite.Options}); err == nil {
			vendor = string(held.Facts().Vendor)
		}
	}
	RunCost(t, providerClient, costv1connect.NewCostServiceClient(server.Client(), server.URL), vendor)
}

func CostManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		SchemaVersion: "provider.v1",
		Slug:          "conformance",
		Apps: []*contractv1.ManifestApp{
			{Name: "web", Framework: &contractv1.Framework{Name: "node"}, Compute: "serverless",
				Domains: []*contractv1.TierDomains{{Tier: environmentv1.Tier_TIER_PRODUCTION, Hostnames: []string{"web.example.com"}}}},
			{Name: "api", Framework: &contractv1.Framework{Name: "go"}, Compute: "container"},
		},
		Functions: []*contractv1.ManifestFunction{
			{LogicalName: "fn--web--entry", App: "web", Framework: &contractv1.Framework{Name: "node"}},
		},
		Containers: []*contractv1.ManifestContainer{
			{App: "api", Image: "registry.example.com/conformance/api@sha256:" + costImageDigest, HealthCheckPath: "/healthz"},
		},
		Resources: []*contractv1.ManifestResource{
			{LogicalName: "main", Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main"}, Config: &contractv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{}}},
			{LogicalName: "uploads", Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "uploads"}, Config: &contractv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}}},
		},
	}
}

func RunCost(t *testing.T, provider contractv1connect.ProviderServiceClient, rates costv1connect.CostServiceClient, vendor string) {
	t.Helper()
	ctx := context.Background()

	set, err := provider.Shape(ctx, &contractv1.ShapeRequest{
		Manifest:    CostManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if connect.CodeOf(err) == connect.CodeUnimplemented {
		t.Skip("this provider describes no resources to price, and says so")
	}
	if err != nil {
		t.Fatalf("Shape() error = %v, want the resources a deploy would create", err)
	}

	t.Run("the shape is a tree every resource hangs on", func(t *testing.T) {
		shapeHoldsTogether(t, set, vendor)
	})

	estimates := map[costv1.Profile]*costv1.Estimate{}
	for _, profile := range costkit.Profiles() {
		est, err := rates.Price(ctx, &costv1.PriceRequest{Resources: set, Usage: &costv1.Usage{Profile: profile}})
		if connect.CodeOf(err) == connect.CodeUnimplemented {
			t.Fatal("this provider shapes a deploy and prices nothing, so a scan would list resources with no number beside them")
		}
		if err != nil {
			t.Fatalf("Price(%s) error = %v", profile, err)
		}
		estimates[profile] = est
	}

	t.Run("every shaped resource is priced or declared free", func(t *testing.T) {
		estimateHoldsTogether(t, set, estimates[costkit.DefaultProfile])
	})
	t.Run("a heavier profile never costs less", func(t *testing.T) {
		profilesAreMonotone(t, estimates)
	})
	t.Run("an unknown property is never priced as zero", func(t *testing.T) {
		unknownIsNeverZero(t, rates, set, estimates[costkit.DefaultProfile])
	})
}

func shapeHoldsTogether(t *testing.T, set *costv1.ResourceSet, vendor string) {
	t.Helper()
	if set.GetSource() != provider.CostSource {
		t.Errorf("source = %q, want %q", set.GetSource(), provider.CostSource)
	}
	scopes := map[string]*costv1.Scope{}
	for _, scope := range set.GetScopes() {
		if scope.GetId() == "" || scope.GetKind() == "" {
			t.Errorf("scope %v carries no id or kind", scope)
		}
		if _, dup := scopes[scope.GetId()]; dup {
			t.Errorf("scope %s appears twice", scope.GetId())
		}
		scopes[scope.GetId()] = scope
	}
	for _, scope := range set.GetScopes() {
		if parent := scope.GetParent(); parent != "" {
			if _, held := scopes[parent]; !held {
				t.Errorf("scope %s hangs on %s, which the tree does not hold", scope.GetId(), parent)
			}
		}
	}
	ids := map[string]bool{}
	for _, resource := range set.GetResources() {
		if resource.GetId() == "" || ids[resource.GetId()] {
			t.Errorf("resource %v carries no id or repeats one", resource)
		}
		ids[resource.GetId()] = true
		if _, held := scopes[resource.GetScope()]; !held {
			t.Errorf("resource %s sits in scope %q, which the tree does not hold", resource.GetId(), resource.GetScope())
		}
		if resource.GetVendor() == "" || resource.GetType() == "" {
			t.Errorf("resource %s carries no vendor or type", resource.GetId())
		}
		if vendor != "" && resource.GetVendor() == vendor && resource.GetRegion() == "" {
			t.Errorf("resource %s is this provider's own and names no region, so its price cannot be looked up", resource.GetId())
		}
		for _, unknown := range resource.GetUnknown() {
			if _, present := resource.GetProperties().GetFields()[unknown]; present {
				t.Errorf("resource %s lists %s as unknown and carries a value for it", resource.GetId(), unknown)
			}
		}
	}
	if len(set.GetResources()) == 0 {
		t.Error("the shape lists no resources for a deploy with two apps and two declared resources")
	}
}

func estimateHoldsTogether(t *testing.T, set *costv1.ResourceSet, est *costv1.Estimate) {
	t.Helper()
	if est.GetCurrency() == "" || est.GetRatesVersion() == "" || est.GetProfile() != costkit.DefaultProfile {
		t.Errorf("estimate header = %s %q %s", est.GetCurrency(), est.GetRatesVersion(), est.GetProfile())
	}
	if len(est.GetResources()) != len(set.GetResources()) {
		t.Fatalf("estimate holds %d resources, the shape %d", len(est.GetResources()), len(set.GetResources()))
	}
	cov := est.GetCoverage()
	if total := cov.GetSupported() + cov.GetFree() + cov.GetUnsupported() + cov.GetNoPrice(); int(total) != len(set.GetResources()) {
		t.Errorf("coverage counts %d, the shape lists %d", total, len(set.GetResources()))
	}
	if cov.GetUnsupported() != 0 {
		t.Errorf("the rate card does not recognise %v, which its own shape listed", cov.GetUnsupportedTypes())
	}
	var fixed, usage decimal.Decimal
	for i, r := range est.GetResources() {
		if r.GetResource() != set.GetResources()[i].GetId() {
			t.Errorf("estimate %d is for %s, the shape's is %s", i, r.GetResource(), set.GetResources()[i].GetId())
		}
		switch r.GetStatus() {
		case costv1.ResourceEstimate_STATUS_PRICED:
			if r.GetMonthlyFixed() == "" || r.GetMonthlyUsage() == "" {
				t.Errorf("%s is priced and carries no totals", r.GetResource())
			}
			fixed = fixed.Add(money(t, r.GetMonthlyFixed()))
			usage = usage.Add(money(t, r.GetMonthlyUsage()))
		case costv1.ResourceEstimate_STATUS_FREE, costv1.ResourceEstimate_STATUS_NO_PRICE:
		default:
			t.Errorf("%s has status %v", r.GetResource(), r.GetStatus())
		}
		for _, c := range r.GetComponents() {
			if c.GetName() == "" || c.GetUnit() == "" {
				t.Errorf("%s carries a component with no name or unit: %v", r.GetResource(), c)
			}
			if c.GetPriceNotFound() && c.GetMonthlyCost() != "" {
				t.Errorf("%s: %s found no price and carries a cost", r.GetResource(), c.GetName())
			}
			if c.GetUsageBased() && c.GetAssumption() == "" && len(c.GetDependsOnUnknown()) == 0 {
				t.Errorf("%s: %s is usage-based and says nothing about the usage it assumed", r.GetResource(), c.GetName())
			}
		}
	}
	if !fixed.Equal(money(t, est.GetMonthlyFixed())) || !usage.Equal(money(t, est.GetMonthlyUsage())) {
		t.Errorf("totals = %s + %s, the resources sum to %s + %s", est.GetMonthlyFixed(), est.GetMonthlyUsage(), fixed, usage)
	}
	var rootFixed, rootUsage decimal.Decimal
	parents := map[string]string{}
	for _, scope := range set.GetScopes() {
		parents[scope.GetId()] = scope.GetParent()
	}
	for _, scope := range est.GetScopes() {
		if parents[scope.GetScope()] == "" {
			rootFixed = rootFixed.Add(money(t, scope.GetMonthlyFixed()))
			rootUsage = rootUsage.Add(money(t, scope.GetMonthlyUsage()))
		}
	}
	if !rootFixed.Equal(fixed) || !rootUsage.Equal(usage) {
		t.Errorf("root scopes roll up to %s + %s, the resources sum to %s + %s", rootFixed, rootUsage, fixed, usage)
	}
}

func profilesAreMonotone(t *testing.T, estimates map[costv1.Profile]*costv1.Estimate) {
	t.Helper()
	light, moderate, heavy := estimates[costv1.Profile_PROFILE_LIGHT], estimates[costv1.Profile_PROFILE_MODERATE], estimates[costv1.Profile_PROFILE_HEAVY]
	if light.GetMonthlyFixed() != moderate.GetMonthlyFixed() || moderate.GetMonthlyFixed() != heavy.GetMonthlyFixed() {
		t.Errorf("fixed cost moves with the profile: %s, %s, %s", light.GetMonthlyFixed(), moderate.GetMonthlyFixed(), heavy.GetMonthlyFixed())
	}
	if money(t, light.GetMonthlyUsage()).GreaterThan(money(t, moderate.GetMonthlyUsage())) || money(t, moderate.GetMonthlyUsage()).GreaterThan(money(t, heavy.GetMonthlyUsage())) {
		t.Errorf("usage cost falls as the profile rises: %s, %s, %s", light.GetMonthlyUsage(), moderate.GetMonthlyUsage(), heavy.GetMonthlyUsage())
	}
	for i := range moderate.GetResources() {
		l, m, h := light.GetResources()[i], moderate.GetResources()[i], heavy.GetResources()[i]
		if m.GetStatus() != costv1.ResourceEstimate_STATUS_PRICED {
			continue
		}
		if money(t, l.GetMonthlyUsage()).GreaterThan(money(t, m.GetMonthlyUsage())) || money(t, m.GetMonthlyUsage()).GreaterThan(money(t, h.GetMonthlyUsage())) {
			t.Errorf("%s: usage cost falls as the profile rises: %s, %s, %s", m.GetResource(), l.GetMonthlyUsage(), m.GetMonthlyUsage(), h.GetMonthlyUsage())
		}
	}
}

func unknownIsNeverZero(t *testing.T, rates costv1connect.CostServiceClient, set *costv1.ResourceSet, priced *costv1.Estimate) {
	t.Helper()
	blurred := proto.Clone(set).(*costv1.ResourceSet)
	for _, resource := range blurred.GetResources() {
		for name := range resource.GetProperties().GetFields() {
			resource.Unknown = append(resource.Unknown, name)
		}
		slices.Sort(resource.Unknown)
		resource.Properties = nil
	}
	est, err := rates.Price(context.Background(), &costv1.PriceRequest{Resources: blurred})
	if err != nil {
		t.Fatalf("Price() of a shape with every property unknown = %v", err)
	}
	for i, r := range est.GetResources() {
		for _, c := range r.GetComponents() {
			if len(c.GetDependsOnUnknown()) > 0 && (c.GetMonthlyCost() != "" || c.GetMonthlyQuantity() != "") {
				t.Errorf("%s: %s reads %v, which is unknown, and still prices at %q for %q", r.GetResource(), c.GetName(), c.GetDependsOnUnknown(), c.GetMonthlyCost(), c.GetMonthlyQuantity())
			}
		}
		if r.GetStatus() != costv1.ResourceEstimate_STATUS_PRICED || priced.GetResources()[i].GetStatus() != costv1.ResourceEstimate_STATUS_PRICED {
			continue
		}
		if money(t, r.GetMonthlyFixed()).GreaterThan(money(t, priced.GetResources()[i].GetMonthlyFixed())) {
			t.Errorf("%s costs more with its properties unknown than known: %s over %s", r.GetResource(), r.GetMonthlyFixed(), priced.GetResources()[i].GetMonthlyFixed())
		}
	}
}

func money(t *testing.T, amount string) decimal.Decimal {
	t.Helper()
	if amount == "" {
		return decimal.Zero
	}
	parsed, err := decimal.NewFromString(amount)
	if err != nil {
		t.Fatalf("%q is no amount of money", amount)
	}
	return parsed
}
