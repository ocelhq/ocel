package conformance

import (
	"context"
	"fmt"
	"net/url"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

const untouchedProject = "conformance-untouched"

func provisionedResourceFaults(ctx context.Context, stacks provider.Stacks, ref provider.StackRef) []string {
	listed, err := stacks.Resources(ctx, provider.StackResources{Ref: ref, NewestRelease: true})
	if err != nil {
		return []string{fmt.Sprintf("Resources() of the stack just provisioned = %v, and `ocel projects show` lists what every stack holds", err)}
	}
	if len(listed) == 0 {
		return []string{"Resources() of the stack just provisioned names nothing, and it holds every resource it declares"}
	}
	return physicalResourceFaults("Resources", listed)
}

func destroyedResourceFaults(ctx context.Context, stacks provider.Stacks, ref provider.StackRef) []string {
	listed, err := stacks.Resources(ctx, provider.StackResources{Ref: ref, NewestRelease: true})
	if err != nil {
		return []string{fmt.Sprintf("Resources() of a stack just destroyed = %v, want nothing named and no error", err)}
	}
	if len(listed) != 0 {
		return []string{fmt.Sprintf("Resources() of a stack just destroyed = %+v, and a teardown leaves nothing behind to name", listed)}
	}
	return nil
}

func projectResourceFaults(ctx context.Context, list func(context.Context, provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error)) []string {
	listed, err := list(ctx, provider.ProjectResourcesRequest{Tier: environment.TierPreview, Project: untouchedProject})
	if err != nil {
		return []string{fmt.Sprintf("ListProjectResources() of a project nothing deployed = %v, want what it holds, which may be nothing", err)}
	}
	return physicalResourceFaults("ListProjectResources", listed)
}

func physicalResourceFaults(verb string, listed []provider.PhysicalResource) []string {
	var faults []string
	for _, resource := range listed {
		if resource.Kind == "" || resource.ID == "" {
			faults = append(faults, fmt.Sprintf("%s() named %+v, and a resource nobody can find again has a kind and an id", verb, resource))
		}
		if resource.ConsoleURL != "" && !isAbsoluteHTTPS(resource.ConsoleURL) {
			faults = append(faults, fmt.Sprintf("%s() linked %s to %q, and a console link is empty or an absolute https URL", verb, resource.ID, resource.ConsoleURL))
		}
	}
	return faults
}

func isAbsoluteHTTPS(link string) bool {
	parsed, err := url.Parse(link)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}
