package scope_test

import (
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/scope"
)

func TestEveryFieldAProviderCallNamesItsProjectWithIsMarkedAsTheProjectSlug(t *testing.T) {
	t.Parallel()

	seen := map[protoreflect.FullName]bool{}
	var walk func(message protoreflect.MessageDescriptor)
	walk = func(message protoreflect.MessageDescriptor) {
		if seen[message.FullName()] {
			return
		}
		seen[message.FullName()] = true
		for i := range message.Fields().Len() {
			field := message.Fields().Get(i)
			if field.Name() == "slug" && !scope.IsProjectSlug(field) {
				t.Errorf("%s names a project and is not marked (common.scope.v1.project_slug), so a session held to one project reads past it", field.FullName())
			}
			if field.Message() != nil {
				walk(field.Message())
			}
		}
	}
	for _, service := range servedServices() {
		for i := range service.Methods().Len() {
			if string(service.ParentFile().Package()) == "provider.contract.v1" || string(service.ParentFile().Package()) == "provider.cost.v1" {
				walk(service.Methods().Get(i).Input())
			}
		}
	}
}

func TestTheProjectSlugsOfARequestAreTheMarkedFieldsWhereverTheySit(t *testing.T) {
	t.Parallel()

	deploy := &contractv1.DeployRequest{
		Manifest: &contractv1.Manifest{
			Slug:    "shop",
			Domains: []*contractv1.TierDomains{{Hostnames: []string{"shop.example.com"}}},
		},
	}
	if got := scope.ListProjectSlugs(deploy); !slices.Equal(got, []string{"shop"}) {
		t.Errorf("ListProjectSlugs(deploy) = %v, want the manifest's slug alone", got)
	}
	if got := scope.ListProjectSlugs(&contractv1.ListProjectsRequest{}); len(got) != 0 {
		t.Errorf("ListProjectSlugs(list projects) = %v, want none", got)
	}
}

func TestTheProjectTiersOfARequestAreTheMarkedFieldsWhereverTheySit(t *testing.T) {
	t.Parallel()

	production, preview := environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW
	for name, tc := range map[string]struct {
		request proto.Message
		want    []environmentv1.Tier
	}{
		"a protection read":  {&contractv1.GetProtectionRequest{Tier: production, Slug: "shop"}, []environmentv1.Tier{production}},
		"a log read":         {&contractv1.ReadLogsRequest{Slug: "shop", Environment: &environmentv1.Environment{Tier: preview}}, []environmentv1.Tier{preview}},
		"a deploy's domains": {&contractv1.DeployRequest{Environment: &environmentv1.Environment{Tier: production}, Manifest: &contractv1.Manifest{Slug: "shop", Domains: []*contractv1.TierDomains{{Tier: preview}}}}, []environmentv1.Tier{production}},
		"a project listing":  {&contractv1.GetProjectRequest{Slug: "shop", Tiers: []environmentv1.Tier{production, preview}}, nil},
	} {
		if got := scope.ListProjectTiers(tc.request); !slices.Equal(got, tc.want) {
			t.Errorf("ListProjectTiers(%s) = %v, want %v", name, got, tc.want)
		}
	}
}

func TestEveryTierAProviderCallForAProjectActsOnIsMarkedAsTheProjectTier(t *testing.T) {
	t.Parallel()

	tier := environmentv1.Tier(0).Descriptor().FullName()
	environment := (&environmentv1.Environment{}).ProtoReflect().Descriptor().FullName()
	for _, service := range servedServices() {
		if string(service.ParentFile().Package()) != "provider.contract.v1" {
			continue
		}
		for i := range service.Methods().Len() {
			request := service.Methods().Get(i).Input()
			for j := range request.Fields().Len() {
				field := request.Fields().Get(j)
				switch {
				case field.IsList():
				case field.Enum() != nil && field.Enum().FullName() == tier && !scope.IsProjectTier(field):
					t.Errorf("%s names the tier its request acts on and is not marked (common.scope.v1.project_tier), so a session held to one tier reads past it", field.FullName())
				case field.Message() != nil && field.Message().FullName() == environment && !scope.IsProjectTier(field.Message().Fields().ByName("tier")):
					t.Errorf("%s names the environment its request acts on, and that environment's tier is not marked (common.scope.v1.project_tier)", field.FullName())
				}
			}
		}
	}
}
