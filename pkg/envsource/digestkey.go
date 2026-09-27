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

	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	digestKeyBytes    = 32
	digestBytes       = 16
	digestKeyAttempts = 3
	everyProject      = "*"
	digestKeyFolder   = "/"
	digestKeyBinding  = "envsource"
	digestKeyName     = "digestkey"
)

type DigestKey struct {
	secret []byte
}

type sealedDigestKey struct {
	Sealed []byte `json:"sealed"`
}

func digestKeyRecord(class edge.Class) records.Name {
	return records.Name{records.RootEnvSourceDigestKey, string(class)}
}

func digestKeyScope(class edge.Class) records.SealScope {
	return records.SealScope{
		Project: everyProject,
		Class:   class,
		Env:     envvars.ClassWideEnvironment,
		Folder:  digestKeyFolder,
		Binding: digestKeyBinding,
		Name:    digestKeyName,
	}
}

func EnsureDigestKey(ctx context.Context, store envvars.Store, class edge.Class) (DigestKey, error) {
	for range digestKeyAttempts {
		recorded, err := records.ReadOrEmpty(ctx, store.Records, digestKeyRecord(class))
		if err != nil {
			return DigestKey{}, err
		}
		if len(recorded.Bytes) > 0 {
			return openDigestKey(ctx, store.Cipher, class, recorded)
		}
		secret := make([]byte, digestKeyBytes)
		if _, err := rand.Read(secret); err != nil {
			return DigestKey{}, err
		}
		sealed, err := store.Cipher.Seal(ctx, digestKeyScope(class), secret)
		if err != nil {
			return DigestKey{}, fmt.Errorf("seal the %s env source digest key: %w", class, err)
		}
		recorded.Bytes, err = json.Marshal(sealedDigestKey{Sealed: sealed})
		if err != nil {
			return DigestKey{}, err
		}
		_, err = store.Records.Write(ctx, recorded)
		if errors.Is(err, records.ErrStale) {
			continue
		}
		if err != nil {
			return DigestKey{}, err
		}
		return DigestKey{secret: secret}, nil
	}
	return DigestKey{}, fmt.Errorf("the %s env source digest key was rewritten under every attempt to read it", class)
}

func openDigestKey(ctx context.Context, cipher records.Cipher, class edge.Class, recorded records.Record) (DigestKey, error) {
	var kept sealedDigestKey
	if err := json.Unmarshal(recorded.Bytes, &kept); err != nil {
		return DigestKey{}, fmt.Errorf("read %s: %w", recorded.Name, err)
	}
	secret, err := cipher.Open(ctx, digestKeyScope(class), kept.Sealed)
	if err != nil {
		return DigestKey{}, fmt.Errorf("open the %s env source digest key: %w", class, err)
	}
	if len(secret) != digestKeyBytes {
		return DigestKey{}, fmt.Errorf("the %s env source digest key opened to %d bytes, want %d", class, len(secret), digestKeyBytes)
	}
	return DigestKey{secret: secret}, nil
}

func (k DigestKey) version(scope envvars.Scope, at envvars.Cell, read Value) (string, error) {
	if len(k.secret) != digestKeyBytes {
		return "", errors.New("an env source value is versioned under its class's digest key, and none was opened")
	}
	mac := hmac.New(sha256.New, k.secret)
	for _, part := range []string{scope.Project, string(scope.Class), at.Folder, at.Key, string(read.Plaintext)} {
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
