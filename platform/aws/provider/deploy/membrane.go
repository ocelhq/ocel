package deploy

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

const providerName = "aws"

func completesUploads(manifest *deploymentsv1.Manifest) bool {
	for _, r := range manifest.GetResources() {
		if r.GetBucket() != nil && !r.GetLinked() {
			return true
		}
	}
	return false
}

type MissingMembraneError struct {
	Resource string
	Type     linksv1.LinkType
	Provider string
}

func (e *MissingMembraneError) Error() string {
	return fmt.Sprintf(
		"%s is a %s, a type an app reaches through the membrane, and the %s provider implements no membrane service for it. "+
			"Drop the resource, or deploy it to a provider that serves it",
		e.Resource, e.Type, e.Provider,
	)
}

func appCrossesMembrane(manifest *deploymentsv1.Manifest, app string) bool {
	used := usedResources(manifest, app)
	for _, r := range manifest.GetResources() {
		if used[r.GetLogicalName()] && naming.CrossesMembrane(r.GetResource().GetType()) {
			return true
		}
	}
	return false
}

func checkMembraneServices(manifest *deploymentsv1.Manifest, serves func(linksv1.LinkType) bool) error {
	for _, r := range manifest.GetResources() {
		typ := r.GetResource().GetType()
		if !naming.CrossesMembrane(typ) || serves(typ) {
			continue
		}
		return &MissingMembraneError{Resource: r.GetLogicalName(), Type: typ, Provider: providerName}
	}
	return nil
}
