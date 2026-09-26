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

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	fingerprintHexLen = 12
)

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
	_, _ = h.Write(size[:])
	_, _ = h.Write(b)
}

func newPromotionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("mint a promotion id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
