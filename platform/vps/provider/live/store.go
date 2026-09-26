package live

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	ClassRoot = "/etc/ocel"
	StateRoot = "/var/lib/ocel"
)

const (
	recordSuffix = ".rec"
	sealKeyFile  = "seal.key"
	sealKeyBytes = 32
	sealNonce    = 12
	sealTag      = 16
)

func RecordsDir(root string, class edge.Class) string {
	return filepath.Join(root, string(class), "records")
}

func KeyPath(root string, class edge.Class) string {
	return filepath.Join(root, string(class), sealKeyFile)
}

var errReadOnly = errors.New("the box's records are read-only here")

type Records struct{ Root string }

func (r Records) Read(_ context.Context, name records.Name) (records.Record, error) {
	class, encoded, err := Located(name)
	if err != nil {
		return records.Record{}, err
	}
	raw, err := os.ReadFile(filepath.Join(RecordsDir(r.Root, class), encoded+recordSuffix))
	if errors.Is(err, fs.ErrNotExist) {
		return records.Record{}, records.ErrNotFound
	}
	if err != nil {
		return records.Record{}, err
	}
	revision, body, err := row(string(raw))
	if err != nil {
		return records.Record{}, fmt.Errorf("%s: %w", name, err)
	}
	return records.Record{Name: name, Bytes: body, Revision: revision}, nil
}

func (r Records) List(_ context.Context, under records.Name) ([]records.Record, error) {
	class, encoded, err := Located(under)
	if err != nil {
		return nil, err
	}
	dir := RecordsDir(r.Root, class)
	var held []records.Record
	err = filepath.WalkDir(filepath.Join(dir, encoded), func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), recordSuffix) {
			return nil
		}
		relative, err := filepath.Rel(dir, strings.TrimSuffix(path, recordSuffix))
		if err != nil {
			return err
		}
		name, err := DecodeName(filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		revision, body, err := row(string(raw))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		held = append(held, records.Record{Name: name, Bytes: body, Revision: revision})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return held, nil
}

func (Records) Write(context.Context, records.Record) (records.Revision, error) {
	return "", errReadOnly
}

func (Records) WritePair(context.Context, records.Record, records.Record) error {
	return errReadOnly
}

func (Records) Remove(context.Context, records.Name, records.Revision) error {
	return errReadOnly
}

func row(raw string) (records.Revision, []byte, error) {
	revision, encoded, split := strings.Cut(strings.TrimRight(raw, "\n"), "\n")
	if !split || revision == "" {
		return "", nil, errors.New("the record on disk carries no revision line")
	}
	body, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", nil, errors.New("the record on disk is not in ocel's format")
	}
	return records.Revision(revision), body, nil
}

type Cipher struct{ Root string }

func (Cipher) Seal(context.Context, records.SealScope, []byte) ([]byte, error) {
	return nil, errors.New("the box seals values only through its helper")
}

func (s Cipher) Open(_ context.Context, at records.SealScope, sealed []byte) ([]byte, error) {
	if at.Class == "" {
		return nil, fmt.Errorf("%s names no class", at.Name)
	}
	key, err := os.ReadFile(KeyPath(s.Root, at.Class))
	if err != nil {
		return nil, fmt.Errorf("read the %s seal key: %w", at.Class, err)
	}
	return Open(key, at, sealed)
}

func Open(key []byte, at records.SealScope, sealed []byte) ([]byte, error) {
	if len(key) != sealKeyBytes {
		return nil, fmt.Errorf("the seal key is %d bytes, want %d", len(key), sealKeyBytes)
	}
	if len(sealed) < sealNonce+sealTag {
		return nil, errors.New("the sealed value is too short")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, sealed[:sealNonce], sealed[sealNonce:], at.AAD())
	if err != nil {
		return nil, fmt.Errorf("%s was not sealed at this coordinate", at.Name)
	}
	return plaintext, nil
}

func Located(name records.Name) (edge.Class, string, error) {
	class, named := providerkit.ClassOf(name)
	if !named {
		return "", "", refusal.Refuse(refusal.CodeInvalid,
			"%s names no class", name)
	}
	encoded, err := EncodeName(name)
	if err != nil {
		return "", "", err
	}
	return class, encoded, nil
}

func EncodeName(name records.Name) (string, error) {
	segments := make([]string, 0, len(name))
	for _, segment := range name {
		if segment == "" {
			return "", refusal.Refuse(refusal.CodeInvalid,
				"%s has an empty segment", name)
		}
		segments = append(segments, encodeSegment(segment))
	}
	return strings.Join(segments, "/"), nil
}

func encodeSegment(segment string) string {
	var written strings.Builder
	for i := 0; i < len(segment); i++ {
		if plain(segment[i]) && (i != 0 || segment[i] != '.') {
			written.WriteByte(segment[i])
			continue
		}
		fmt.Fprintf(&written, "%%%02X", segment[i])
	}
	return written.String()
}

func plain(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	default:
		return c == '-' || c == '_' || c == '.'
	}
}

func DecodeName(encoded string) (records.Name, error) {
	var name records.Name
	for _, segment := range strings.Split(encoded, "/") {
		decoded, err := decodeSegment(segment)
		if err != nil {
			return nil, err
		}
		name = append(name, decoded)
	}
	return name, nil
}

func decodeSegment(segment string) (string, error) {
	var written strings.Builder
	for i := 0; i < len(segment); i++ {
		if segment[i] != '%' {
			written.WriteByte(segment[i])
			continue
		}
		if i+2 >= len(segment) {
			return "", refusal.Refuse(refusal.CodeDenied,
				"the records helper returned %q, which ocel did not write", segment)
		}
		value, err := strconv.ParseUint(segment[i+1:i+3], 16, 8)
		if err != nil {
			return "", refusal.Refuse(refusal.CodeDenied,
				"the records helper returned %q, which ocel did not write", segment)
		}
		written.WriteByte(byte(value))
		i += 2
	}
	return written.String(), nil
}

var (
	_ records.Store  = Records{}
	_ records.Cipher = Cipher{}
)
