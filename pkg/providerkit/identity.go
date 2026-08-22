package providerkit

import (
	"crypto/rand"
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
)

const (
	identitySeparator = "~"

	fingerprintHexLen = 12
)

type Build struct {
	deploymentID string
	fingerprint  string
}

func NewBuild(deploymentID, environment, values string) (Build, error) {
	if err := naming.ValidateDeploymentID(deploymentID); err != nil {
		return Build{}, Refuse(CodeInvalid, "deployment identity: %s", err.Error())
	}
	if environment == "" {
		return Build{}, Refuse(CodeInvalid, "deployment identity for %q requires an environment name", deploymentID)
	}
	h := sha256.New()
	writeLenPrefixed(h, []byte(environment))
	writeLenPrefixed(h, []byte(values))
	return Build{
		deploymentID: deploymentID,
		fingerprint:  hex.EncodeToString(h.Sum(nil))[:fingerprintHexLen],
	}, nil
}

func ParseBuild(rendered string) (Build, error) {
	deploymentID, fingerprint, split := strings.Cut(rendered, identitySeparator)
	if !split || deploymentID == "" || fingerprint == "" {
		return Build{}, fmt.Errorf("deployment identity %q must be a deployment id and a fingerprint joined by %q", rendered, identitySeparator)
	}
	if strings.Contains(fingerprint, identitySeparator) {
		return Build{}, fmt.Errorf("deployment identity %q carries more than one %q", rendered, identitySeparator)
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
		writeLenPrefixed(h, []byte(variable.GetKey()))
		writeLenPrefixed(h, []byte(variable.GetFolder()))
		writeLenPrefixed(h, []byte(strconv.FormatInt(variable.GetVersion(), 10)))
	}
	return hex.EncodeToString(h.Sum(nil))[:fingerprintHexLen]
}

func writeLenPrefixed(h io.Writer, b []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(b)))
	h.Write(size[:])
	h.Write(b)
}

func newPromotionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("mint a promotion id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
