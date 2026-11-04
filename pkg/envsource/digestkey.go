package envsource

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const (
	digestKeyBytes = 32
	digestBytes    = 16
	digestKeyName  = "digestkey"
)

type DigestKey struct {
	secret []byte
}

type sealedDigestKey struct {
	Sealed []byte `json:"sealed"`
}

const digestKeyPurpose = "env source digest key"

func digestKeyAt(tier environment.Tier) records.Key {
	return records.Partition{Tier: tier, Root: RootDigestKey}.Key(digestKeyName)
}

func newDigestKeyAssociatedData(tier environment.Tier) seal.AssociatedData {
	return seal.Bind(digestKeyPurpose, string(tier))
}

func EnsureDigestKey(ctx context.Context, store variablestore.Store, tier environment.Tier) (DigestKey, error) {
	var key DigestKey
	err := records.Change(ctx, store.KeyValues, digestKeyAt(tier), func(recorded records.Entry) ([]byte, bool, error) {
		if len(recorded.Value) > 0 {
			opened, err := openDigestKey(ctx, store.Cipher, tier, recorded)
			key = opened
			return nil, false, err
		}
		secret := make([]byte, digestKeyBytes)
		if _, err := rand.Read(secret); err != nil {
			return nil, false, err
		}
		sealed, err := store.Cipher.Seal(ctx, tier, newDigestKeyAssociatedData(tier), secret)
		if err != nil {
			return nil, false, fmt.Errorf("seal the %s env source digest key: %w", tier, err)
		}
		value, err := json.Marshal(sealedDigestKey{Sealed: sealed})
		key = DigestKey{secret: secret}
		return value, true, err
	})
	if errors.Is(err, records.ErrContended) {
		return DigestKey{}, fmt.Errorf("the %s env source digest key was rewritten under every attempt to read it", tier)
	}
	if err != nil {
		return DigestKey{}, err
	}
	return key, nil
}

func ForgetDigestKey(ctx context.Context, store records.Store, tier environment.Tier) error {
	return records.Forget(ctx, store, digestKeyAt(tier))
}

func openDigestKey(ctx context.Context, cipher seal.Cipher, tier environment.Tier, recorded records.Entry) (DigestKey, error) {
	var kept sealedDigestKey
	if err := json.Unmarshal(recorded.Value, &kept); err != nil {
		return DigestKey{}, fmt.Errorf("read %s: %w", recorded.Key, err)
	}
	secret, err := cipher.Open(ctx, tier, newDigestKeyAssociatedData(tier), kept.Sealed)
	if err != nil {
		return DigestKey{}, fmt.Errorf("open the %s env source digest key: %w", tier, err)
	}
	if len(secret) != digestKeyBytes {
		return DigestKey{}, fmt.Errorf("the %s env source digest key opened to %d bytes, want %d", tier, len(secret), digestKeyBytes)
	}
	return DigestKey{secret: secret}, nil
}

func (k DigestKey) version(scope variablestore.Scope, at variablestore.Cell, read Value) (string, error) {
	if len(k.secret) != digestKeyBytes {
		return "", errors.New("an env source value is versioned under its tier's digest key, and none was opened")
	}
	mac := hmac.New(sha256.New, k.secret)
	for _, part := range []string{scope.Project, string(scope.Tier), at.Folder, at.Key, string(read.Plaintext)} {
		writeLengthPrefixed(mac, part)
	}
	digest := hex.EncodeToString(mac.Sum(nil)[:digestBytes])
	if read.Version == "" {
		return digest, nil
	}
	return read.Version + "#" + digest, nil
}

func sourceVersionOf(copiedVersion string) string {
	sourceVersion, _, _ := strings.Cut(copiedVersion, "#")
	return sourceVersion
}

func writeLengthPrefixed(h hash.Hash, part string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(part)))
	h.Write(length[:])
	h.Write([]byte(part))
}

func ownerOfDigestKey(path []string) (records.Owner, bool) {
	if len(path) == 1 && path[0] == digestKeyName {
		return records.Owner{Shared: true}, true
	}
	return records.Owner{}, false
}

func OwnerOfAssociatedData(bound seal.AssociatedData) (records.Owner, bool) {
	if bound.Purpose() == digestKeyPurpose {
		return records.Owner{Shared: true}, true
	}
	return records.Owner{}, false
}
