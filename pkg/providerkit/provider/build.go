package provider

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

const (
	identitySeparator = "~"
	FingerprintHexLen = 12
)

type Build struct {
	deploymentID string
	fingerprint  string
}

func NewBuild(deploymentID, environment, values string) (Build, error) {
	if err := naming.ValidateDeploymentID(deploymentID); err != nil {
		return Build{}, refusal.Refuse(refusal.CodeInvalid, "deployment identity: %s", err.Error())
	}
	if environment == "" {
		return Build{}, refusal.Refuse(refusal.CodeInvalid, "deployment identity for %q requires an environment name", deploymentID)
	}
	h := sha256.New()
	WriteLenPrefixed(h, []byte(environment))
	WriteLenPrefixed(h, []byte(values))
	return Build{
		deploymentID: deploymentID,
		fingerprint:  hex.EncodeToString(h.Sum(nil))[:FingerprintHexLen],
	}, nil
}

func ParseBuild(rendered string) (Build, error) {
	deploymentID, fingerprint, split := strings.Cut(rendered, identitySeparator)
	if !split || deploymentID == "" || fingerprint == "" {
		return Build{}, fmt.Errorf("deployment identity %q must be a deployment id and a fingerprint joined by %q", rendered, identitySeparator)
	}
	if strings.Contains(fingerprint, identitySeparator) {
		return Build{}, fmt.Errorf("deployment identity %q contains more than one %q", rendered, identitySeparator)
	}
	return Build{deploymentID: deploymentID, fingerprint: fingerprint}, nil
}

func (id Build) DeploymentID() string { return id.deploymentID }

func (id Build) Fingerprint() string { return id.fingerprint }

func (id Build) IsZero() bool { return id.deploymentID == "" && id.fingerprint == "" }

func (id Build) String() string { return id.deploymentID + identitySeparator + id.fingerprint }

func (id Build) Release() naming.Release {
	return naming.NewRelease(id.deploymentID, id.fingerprint)
}

func WriteLenPrefixed(h io.Writer, b []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(b)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(b)
}
