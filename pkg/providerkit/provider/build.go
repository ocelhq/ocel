package provider

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
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

func FingerprintVariables(variables []*contractv1.ManifestVariable) string {
	if len(variables) == 0 {
		return ""
	}
	ordered := slices.Clone(variables)
	slices.SortFunc(ordered, func(a, b *contractv1.ManifestVariable) int {
		if a.GetFolder() != b.GetFolder() {
			return strings.Compare(a.GetFolder(), b.GetFolder())
		}
		return strings.Compare(a.GetKey(), b.GetKey())
	})
	h := sha256.New()
	for _, variable := range ordered {
		WriteLenPrefixed(h, []byte(variable.GetKey()))
		WriteLenPrefixed(h, []byte(variable.GetFolder()))
		WriteLenPrefixed(h, []byte(strconv.FormatInt(variable.GetVersion(), 10)))
	}
	return hex.EncodeToString(h.Sum(nil))[:FingerprintHexLen]
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
