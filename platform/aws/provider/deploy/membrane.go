package deploy

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const providerName = "aws"

func completesUploads(manifest *contractv1.Manifest) bool {
	for _, r := range manifest.GetResources() {
		if r.GetBucket() != nil && !r.GetLinked() {
			return true
		}
	}
	return false
}

type MissingMembraneError struct {
	Resource string
	Type     providerkit.LinkType
	Provider string
}

func (e *MissingMembraneError) Error() string {
	return fmt.Sprintf(
		"%s is a %s, a type an app reaches through the membrane, and the %s provider implements no membrane service for it. "+
			"Drop the resource, or deploy it to a provider that serves it",
		e.Resource, e.Type, e.Provider,
	)
}

func appCrossesMembrane(manifest *contractv1.Manifest, app string) bool {
	used := usedResources(manifest, app)
	for _, r := range manifest.GetResources() {
		if used[r.GetLogicalName()] && naming.CrossesMembrane(r.GetResource().GetType()) {
			return true
		}
	}
	return false
}

func checkMembraneServices(plan providerkit.StackPlan, serves func(linksv1.LinkType) bool) error {
	for _, resource := range plan.Resources {
		if err := membraneServed(resource.Declared, resource.Type, serves); err != nil {
			return err
		}
	}
	if plan.App == nil {
		return nil
	}
	for _, link := range plan.App.Grants {
		if err := membraneServed(linkResource(link), link.Type, serves); err != nil {
			return err
		}
	}
	return nil
}

func membraneServed(resource string, kind providerkit.LinkType, serves func(linksv1.LinkType) bool) error {
	wire := wireLinkType(kind)
	if !naming.CrossesMembrane(wire) || serves(wire) {
		return nil
	}
	return &MissingMembraneError{Resource: resource, Type: kind, Provider: providerName}
}
