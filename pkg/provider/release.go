package provider

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	releaseSeparator  = "~"
	FingerprintHexLen = 12
)

type Release struct {
	buildID     string
	fingerprint string
}

func NewRelease(buildID, promotionID, environment, values string) (Release, error) {
	if err := naming.ValidateBuildID(buildID); err != nil {
		return Release{}, refusal.Refuse(refusal.CodeInvalid, "release: %s", err.Error())
	}
	if promotionID == "" {
		return Release{}, refusal.Refuse(refusal.CodeInvalid, "release of build %q requires the promotion its deploy makes, so no two deploys claim one release", buildID)
	}
	if environment == "" {
		return Release{}, refusal.Refuse(refusal.CodeInvalid, "release of build %q requires an environment name", buildID)
	}
	h := sha256.New()
	WriteLenPrefixed(h, []byte(promotionID))
	WriteLenPrefixed(h, []byte(environment))
	WriteLenPrefixed(h, []byte(values))
	return Release{
		buildID:     buildID,
		fingerprint: hex.EncodeToString(h.Sum(nil))[:FingerprintHexLen],
	}, nil
}

func ParseRelease(rendered string) (Release, error) {
	buildID, fingerprint, split := strings.Cut(rendered, releaseSeparator)
	if !split || buildID == "" || fingerprint == "" {
		return Release{}, fmt.Errorf("release %q must be a build id and a fingerprint joined by %q", rendered, releaseSeparator)
	}
	if strings.Contains(fingerprint, releaseSeparator) {
		return Release{}, fmt.Errorf("release %q contains more than one %q", rendered, releaseSeparator)
	}
	return Release{buildID: buildID, fingerprint: fingerprint}, nil
}

func (r Release) BuildID() string { return r.buildID }

func (r Release) Fingerprint() string { return r.fingerprint }

func (r Release) IsZero() bool { return r.buildID == "" && r.fingerprint == "" }

func (r Release) String() string { return r.buildID + releaseSeparator + r.fingerprint }

func (r Release) Token() naming.ReleaseToken {
	return naming.NewReleaseToken(r.buildID, r.fingerprint)
}

func WriteLenPrefixed(h io.Writer, b []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(b)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(b)
}
