package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

type listedStacks struct {
	provider.Stacks
	listed []provider.PhysicalResource
	err    error
}

func (s listedStacks) Resources(context.Context, provider.StackResources) ([]provider.PhysicalResource, error) {
	return s.listed, s.err
}

var listedRef = provider.StackRef{Project: "conformance", Tier: environment.TierPreview, Name: naming.InfraStack("lifecycle")}

func TestAProvisionedStackNamingResourcesWithKindsIDsAndHTTPSLinksPasses(t *testing.T) {
	stacks := listedStacks{listed: []provider.PhysicalResource{
		{Kind: "fake:bucket", ID: "c-bucket"},
		{Kind: "fake:function", ID: "c-function", Location: "nowhere", ConsoleURL: "https://console.fake.example/c-function"},
	}}
	if found := provisionedResourceFaults(context.Background(), stacks, listedRef); len(found) != 0 {
		t.Errorf("provisionedResourceFaults() = %v, want none", found)
	}
}

func TestAProvisionedStackThatNamesNoResourceFails(t *testing.T) {
	if found := provisionedResourceFaults(context.Background(), listedStacks{}, listedRef); len(found) == 0 {
		t.Error("provisionedResourceFaults() = none, want a provisioned stack that names nothing flagged, since `ocel projects show` would list it empty")
	}
}

func TestAProvisionedStackWhoseResourcesCannotBeListedFails(t *testing.T) {
	stacks := listedStacks{listed: []provider.PhysicalResource{{Kind: "fake:bucket", ID: "c-bucket"}}, err: errors.New("unreachable")}
	if found := provisionedResourceFaults(context.Background(), stacks, listedRef); len(found) == 0 {
		t.Error("provisionedResourceFaults() = none, want the error Resources() returned flagged")
	}
}

func TestAProvisionedStackResourceWithoutAKindOrIDFails(t *testing.T) {
	for name, resource := range map[string]provider.PhysicalResource{
		"no kind": {ID: "c-bucket"},
		"no id":   {Kind: "fake:bucket"},
	} {
		t.Run(name, func(t *testing.T) {
			stacks := listedStacks{listed: []provider.PhysicalResource{resource}}
			if found := provisionedResourceFaults(context.Background(), stacks, listedRef); len(found) == 0 {
				t.Errorf("provisionedResourceFaults() of %+v = none, want it flagged", resource)
			}
		})
	}
}

func TestAConsoleLinkThatIsNotAnAbsoluteHTTPSURLFails(t *testing.T) {
	for _, link := range []string{"http://console.fake.example/c-bucket", "/c-bucket", "https:///c-bucket", "console.fake.example/c-bucket", "https://console.fake.example/%zz"} {
		t.Run(link, func(t *testing.T) {
			stacks := listedStacks{listed: []provider.PhysicalResource{{Kind: "fake:bucket", ID: "c-bucket", ConsoleURL: link}}}
			if found := provisionedResourceFaults(context.Background(), stacks, listedRef); len(found) == 0 {
				t.Errorf("provisionedResourceFaults() of console link %q = none, want it flagged", link)
			}
		})
	}
}

func TestADestroyedStackNamingNothingPasses(t *testing.T) {
	if found := destroyedResourceFaults(context.Background(), listedStacks{}, listedRef); len(found) != 0 {
		t.Errorf("destroyedResourceFaults() = %v, want none", found)
	}
}

func TestADestroyedStackThatStillNamesAResourceFails(t *testing.T) {
	stacks := listedStacks{listed: []provider.PhysicalResource{{Kind: "fake:bucket", ID: "c-bucket"}}}
	if found := destroyedResourceFaults(context.Background(), stacks, listedRef); len(found) == 0 {
		t.Error("destroyedResourceFaults() = none, want a resource named after its stack's teardown flagged")
	}
}

func TestADestroyedStackWhoseResourcesCannotBeListedFails(t *testing.T) {
	if found := destroyedResourceFaults(context.Background(), listedStacks{err: errors.New("stack not found")}, listedRef); len(found) == 0 {
		t.Error("destroyedResourceFaults() = none, want the error Resources() returned for a destroyed stack flagged")
	}
}

func TestAProjectNothingDeployedListingNothingPasses(t *testing.T) {
	list := func(context.Context, provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
		return nil, nil
	}
	if found := projectResourceFaults(context.Background(), list); len(found) != 0 {
		t.Errorf("projectResourceFaults() = %v, want none", found)
	}
}

func TestListingAProjectNothingDeployedThatErrorsFails(t *testing.T) {
	list := func(context.Context, provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
		return nil, errors.New("no such project")
	}
	if found := projectResourceFaults(context.Background(), list); len(found) == 0 {
		t.Error("projectResourceFaults() = none, want the error a project nothing deployed got flagged")
	}
}

func TestAResourceListedForAProjectWithoutAKindOrIDFails(t *testing.T) {
	for name, resource := range map[string]provider.PhysicalResource{
		"no kind": {ID: "ocel/conformance-untouched"},
		"no id":   {Kind: "fake:repository"},
	} {
		t.Run(name, func(t *testing.T) {
			list := func(context.Context, provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
				return []provider.PhysicalResource{resource}, nil
			}
			if found := projectResourceFaults(context.Background(), list); len(found) == 0 {
				t.Errorf("projectResourceFaults() of %+v = none, want it flagged", resource)
			}
		})
	}
}

func TestListingAProjectAsksForTheProjectNothingDeployed(t *testing.T) {
	var asked provider.ProjectResourcesRequest
	list := func(_ context.Context, req provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
		asked = req
		return nil, nil
	}
	projectResourceFaults(context.Background(), list)
	if asked.Project != untouchedProject || asked.Tier != environment.TierPreview {
		t.Errorf("projectResourceFaults() asked for %+v, want project %q in the preview tier", asked, untouchedProject)
	}
}
