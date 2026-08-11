package deploy

import (
	"fmt"
	"strings"
)

const identitySeparator = "~"

type Identity struct {
	buildID     string
	fingerprint string
}

func (id Identity) BuildID() string { return id.buildID }

func (id Identity) Fingerprint() string { return id.fingerprint }

type Identities map[string]Identity

func NewIdentity(buildID, fingerprint string) (Identity, error) {
	if buildID == "" {
		return Identity{}, fmt.Errorf("deployment identity requires a build id")
	}
	for label, part := range map[string]string{"build id": buildID, "value fingerprint": fingerprint} {
		if strings.Contains(part, identitySeparator) {
			return Identity{}, fmt.Errorf("%s %q contains the reserved character %q", label, part, identitySeparator)
		}
	}
	return Identity{buildID: buildID, fingerprint: fingerprint}, nil
}

func (id Identity) String() string {
	if id.fingerprint == "" {
		return id.buildID
	}
	return id.buildID + identitySeparator + id.fingerprint
}

func ParseIdentity(s string) (Identity, error) {
	buildID, fingerprint, split := strings.Cut(s, identitySeparator)
	if split && fingerprint == "" {
		return Identity{}, fmt.Errorf("deployment identity %q has an empty value fingerprint", s)
	}
	return NewIdentity(buildID, fingerprint)
}
