// Deployment identity: what tells one Deployment of an app from another.
//
// A framework build id alone cannot do it. A vars-only deploy (rotating a
// baked value) reuses the existing build output, so it has no new build id, yet
// it must mint its own Deployment: records are immutable, keyed by identity, and
// the serving workers cache them. So an identity is the build id plus an
// optional fingerprint of the resolved baked values.
//
// The identity keys the Deployment record and names the app-deploy stack. The
// static-asset, ISR and edge-bundle prefixes stay keyed by the build id alone:
// those bytes are exactly what the build produced, and two Deployments of one
// build legitimately share them.
package deploy

import (
	"fmt"
	"strings"
)

// identitySeparator joins a build id to its fingerprint. Reserved: a build id
// or fingerprint containing it is refused at construction, which is what makes
// the rendered form unambiguous to parse and keeps a fingerprinted identity
// from ever rendering as some other Deployment's bare build id.
const identitySeparator = "~"

// DeploymentIdentity identifies one Deployment of one app. Build the value
// through NewDeploymentIdentity or ParseDeploymentIdentity so its parts are
// checked once, in one place.
type DeploymentIdentity struct {
	// BuildID is the framework build's own id — a Next app's routing-manifest
	// buildId, or a host-minted id for a framework with none.
	BuildID string
	// Fingerprint distinguishes Deployments sharing a build: a digest of the
	// resolved values baked into this one. Empty when nothing is baked, which
	// renders the identity as the bare build id.
	Fingerprint string
}

// DeploymentIdentities maps each app name (ManifestApp.name) to the identity of
// the Deployment this deploy produces for it. The deploy host assigns these
// before planning; BuildPlan only arranges them.
type DeploymentIdentities map[string]DeploymentIdentity

// NewDeploymentIdentity is the one place an identity is derived from a build id
// and a fingerprint. An empty build id, or either part carrying the reserved
// separator, is an error rather than a silently ambiguous identity.
func NewDeploymentIdentity(buildID, fingerprint string) (DeploymentIdentity, error) {
	if buildID == "" {
		return DeploymentIdentity{}, fmt.Errorf("deployment identity requires a build id")
	}
	for label, part := range map[string]string{"build id": buildID, "value fingerprint": fingerprint} {
		if strings.Contains(part, identitySeparator) {
			return DeploymentIdentity{}, fmt.Errorf("%s %q contains the reserved character %q", label, part, identitySeparator)
		}
	}
	return DeploymentIdentity{BuildID: buildID, Fingerprint: fingerprint}, nil
}

// String renders the identity as the single token that keys a Deployment
// record: the bare build id when nothing is baked, so an identity without a
// fingerprint is byte-for-byte the build id it came from.
func (id DeploymentIdentity) String() string {
	if id.Fingerprint == "" {
		return id.BuildID
	}
	return id.BuildID + identitySeparator + id.Fingerprint
}

// ParseDeploymentIdentity recovers an identity from its rendered form — how the
// prune path gets back the build id a store record key names, so it can reclaim
// the build-keyed storage prefixes as well as the identity-keyed stack.
func ParseDeploymentIdentity(s string) (DeploymentIdentity, error) {
	buildID, fingerprint, split := strings.Cut(s, identitySeparator)
	if split && fingerprint == "" {
		return DeploymentIdentity{}, fmt.Errorf("deployment identity %q has an empty value fingerprint", s)
	}
	return NewDeploymentIdentity(buildID, fingerprint)
}
