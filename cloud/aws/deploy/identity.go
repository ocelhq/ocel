package deploy

import (
	"fmt"
	"strings"
)

const identitySeparator = "~"

type DeploymentIdentity struct {
	buildID     string
	fingerprint string
}

func (id DeploymentIdentity) BuildID() string { return id.buildID }

func (id DeploymentIdentity) Fingerprint() string { return id.fingerprint }

type DeploymentIdentities map[string]DeploymentIdentity

func NewDeploymentIdentity(buildID, fingerprint string) (DeploymentIdentity, error) {
	if buildID == "" {
		return DeploymentIdentity{}, fmt.Errorf("deployment identity requires a build id")
	}
	for label, part := range map[string]string{"build id": buildID, "value fingerprint": fingerprint} {
		if strings.Contains(part, identitySeparator) {
			return DeploymentIdentity{}, fmt.Errorf("%s %q contains the reserved character %q", label, part, identitySeparator)
		}
	}
	return DeploymentIdentity{buildID: buildID, fingerprint: fingerprint}, nil
}

func (id DeploymentIdentity) String() string {
	if id.fingerprint == "" {
		return id.buildID
	}
	return id.buildID + identitySeparator + id.fingerprint
}

func ParseDeploymentIdentity(s string) (DeploymentIdentity, error) {
	buildID, fingerprint, split := strings.Cut(s, identitySeparator)
	if split && fingerprint == "" {
		return DeploymentIdentity{}, fmt.Errorf("deployment identity %q has an empty value fingerprint", s)
	}
	return NewDeploymentIdentity(buildID, fingerprint)
}
