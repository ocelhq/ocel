package deploy

import (
	"fmt"
	"strings"
)

const identitySeparator = "~"

type Identity struct {
	deploymentID string
	buildID      string
	fingerprint  string
}

func (id Identity) DeploymentID() string { return id.deploymentID }

func (id Identity) BuildID() string { return id.buildID }

func (id Identity) Fingerprint() string { return id.fingerprint }

type Identities map[string]Identity

func NewIdentity(deploymentID, buildID, fingerprint string) (Identity, error) {
	if deploymentID == "" {
		return Identity{}, fmt.Errorf("deployment identity requires a deployment id")
	}
	if buildID == "" {
		return Identity{}, fmt.Errorf("deployment identity requires a build id")
	}
	for label, part := range map[string]string{
		"deployment id":     deploymentID,
		"build id":          buildID,
		"value fingerprint": fingerprint,
	} {
		if strings.Contains(part, identitySeparator) {
			return Identity{}, fmt.Errorf("%s %q contains the reserved character %q", label, part, identitySeparator)
		}
	}
	return Identity{deploymentID: deploymentID, buildID: buildID, fingerprint: fingerprint}, nil
}

func (id Identity) String() string {
	if id.fingerprint == "" {
		return id.deploymentID + identitySeparator + id.buildID
	}
	return id.deploymentID + identitySeparator + id.buildID + identitySeparator + id.fingerprint
}

func ParseIdentity(s string) (Identity, error) {
	deploymentID, rest, split := strings.Cut(s, identitySeparator)
	if !split {
		return Identity{}, fmt.Errorf("deployment identity %q carries no build id", s)
	}
	buildID, fingerprint, split := strings.Cut(rest, identitySeparator)
	if split && fingerprint == "" {
		return Identity{}, fmt.Errorf("deployment identity %q has an empty value fingerprint", s)
	}
	return NewIdentity(deploymentID, buildID, fingerprint)
}
