package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

const identitySeparator = "~"

type Identity struct {
	deploymentID string
	fingerprint  string
}

func (id Identity) DeploymentID() string { return id.deploymentID }

func (id Identity) Fingerprint() string { return id.fingerprint }

type Identities map[string]Identity

func NewIdentity(deploymentID, environment, values string) (Identity, error) {
	if err := naming.ValidateDeploymentID(deploymentID); err != nil {
		return Identity{}, fmt.Errorf("deployment identity: %w", err)
	}
	if environment == "" {
		return Identity{}, fmt.Errorf("deployment identity for %q requires an environment name", deploymentID)
	}
	return Identity{deploymentID: deploymentID, fingerprint: fingerprintIdentity(environment, values)}, nil
}

func fingerprintIdentity(environment, values string) string {
	h := sha256.New()
	writeLenPrefixed(h, []byte(environment))
	writeLenPrefixed(h, []byte(values))
	return hex.EncodeToString(h.Sum(nil))[:fingerprintValuesHexLen]
}

func (id Identity) String() string {
	return id.deploymentID + identitySeparator + id.fingerprint
}

func ParseIdentity(s string) (Identity, error) {
	deploymentID, fingerprint, split := strings.Cut(s, identitySeparator)
	if !split || deploymentID == "" || fingerprint == "" {
		return Identity{}, fmt.Errorf("deployment identity %q must be a deployment id and a fingerprint joined by %q", s, identitySeparator)
	}
	if strings.Contains(fingerprint, identitySeparator) {
		return Identity{}, fmt.Errorf("deployment identity %q carries more than one %q", s, identitySeparator)
	}
	return Identity{deploymentID: deploymentID, fingerprint: fingerprint}, nil
}
