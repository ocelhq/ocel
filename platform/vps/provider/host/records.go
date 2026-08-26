package host

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

//go:embed records.sh
var recordsScript []byte

const (
	exitNoRecord = 3
	exitStale    = 4
)

const (
	revisionWidth = 32
	revisionHex   = "0123456789abcdef"
	acknowledged  = "removed"
)

type Records struct{ host *Host }

func NewRecords(h *Host) *Records { return &Records{host: h} }

func (r *Records) tier(ctx context.Context, name providerkit.RecordName) (providerkit.Class, string, bool, error) {
	class, encoded, err := located(name)
	if err != nil {
		return "", "", false, err
	}
	stood, err := r.host.holds(ctx, class)
	if err != nil {
		return "", "", false, err
	}
	return class, encoded, stood, nil
}

func unbootstrapped(class providerkit.Class) error {
	return providerkit.Refuse(providerkit.CodeNotReady,
		"this host has no ocel bootstrap, so there is nowhere to keep a record.\nRun `%s` to write one, then try again",
		providerkit.BootstrapCommand(class))
}

func (r *Records) Read(ctx context.Context, name providerkit.RecordName) (providerkit.Record, error) {
	class, encoded, stood, err := r.tier(ctx, name)
	if err != nil {
		return providerkit.Record{}, err
	}
	if !stood {
		return providerkit.Record{}, providerkit.ErrNoRecord
	}
	rendered, err := r.helper(ctx, class, nil, "read", encoded)
	if err != nil {
		return providerkit.Record{}, err
	}
	revision, body, err := readRow(rendered)
	if err != nil {
		return providerkit.Record{}, err
	}
	return providerkit.Record{Name: name, Bytes: body, Revision: revision}, nil
}

func (r *Records) Write(ctx context.Context, record providerkit.Record) (providerkit.Revision, error) {
	class, encoded, stood, err := r.tier(ctx, record.Name)
	if err != nil {
		return "", err
	}
	if !stood {
		return "", unbootstrapped(class)
	}
	rendered, err := r.helper(ctx, class, body(record), "write", encoded, string(record.Revision))
	if err != nil {
		return "", err
	}
	return minted(rendered)
}

func (r *Records) WritePair(ctx context.Context, first, second providerkit.Record) error {
	class, one, stood, err := r.tier(ctx, first.Name)
	if err != nil {
		return err
	}
	beside, two, err := located(second.Name)
	if err != nil {
		return err
	}
	if beside != class {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s and %s belong to different classes, and this host writes a pair under one lock",
			first.Name, second.Name)
	}
	if !stood {
		return unbootstrapped(class)
	}
	fed := append(body(first), body(second)...)
	rendered, err := r.helper(ctx, class, fed, "pair", one, string(first.Revision), two, string(second.Revision))
	if err != nil {
		return err
	}
	left, right, split := strings.Cut(strings.TrimSpace(rendered), "\t")
	if !split {
		return providerkit.Refuse(providerkit.CodeDenied,
			"the records helper answered a pair with %q, and ocel cannot tell what landed", rendered)
	}
	if _, err := minted(left); err != nil {
		return err
	}
	_, err = minted(right)
	return err
}

func (r *Records) Remove(ctx context.Context, name providerkit.RecordName, expected providerkit.Revision) error {
	class, encoded, stood, err := r.tier(ctx, name)
	if err != nil {
		return err
	}
	if !stood {
		return providerkit.ErrNoRecord
	}
	rendered, err := r.helper(ctx, class, nil, "remove", encoded, string(expected))
	if err != nil {
		return err
	}
	if strings.TrimSpace(rendered) != acknowledged {
		return providerkit.Refuse(providerkit.CodeDenied,
			"the records helper took %s and would not say so, so ocel cannot call it gone", name)
	}
	return nil
}

func (r *Records) List(ctx context.Context, under providerkit.RecordName) ([]providerkit.Record, error) {
	class, encoded, stood, err := r.tier(ctx, under)
	if err != nil {
		return nil, err
	}
	if !stood {
		return nil, nil
	}
	rendered, err := r.helper(ctx, class, nil, "list", encoded)
	if err != nil {
		return nil, err
	}
	var held []providerkit.Record
	for _, line := range strings.Split(strings.TrimSpace(rendered), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		columns := strings.SplitN(line, "\t", 3)
		if len(columns) != 3 {
			return nil, providerkit.Refuse(providerkit.CodeDenied,
				"the records helper listed a row ocel cannot read: %q", line)
		}
		name, err := decode(columns[0])
		if err != nil {
			return nil, err
		}
		bytes, err := base64.StdEncoding.DecodeString(columns[2])
		if err != nil {
			return nil, providerkit.Refuse(providerkit.CodeDenied, "%s is stored as no record ocel wrote", name)
		}
		held = append(held, providerkit.Record{Name: name, Bytes: bytes, Revision: providerkit.Revision(columns[1])})
	}
	return held, nil
}

func (r *Records) helper(ctx context.Context, class providerkit.Class, stdin []byte, args ...string) (string, error) {
	command := quoted(recordsHelper) + " " + quoted(string(class))
	for _, arg := range args {
		command += " " + quoted(arg)
	}
	elevation, err := r.host.elevate(ctx)
	if err != nil {
		return "", err
	}
	result, err := r.host.exec(ctx, command, stdin, elevation)
	if err != nil {
		return "", err
	}
	switch result.Code {
	case 0:
		return result.Stdout, nil
	case exitNoRecord:
		return "", providerkit.ErrNoRecord
	case exitStale:
		return "", providerkit.ErrStale
	default:
		return "", r.host.refuse("records "+args[0], result)
	}
}

func minted(rendered string) (providerkit.Revision, error) {
	revision := strings.TrimSpace(rendered)
	if len(revision) != revisionWidth || strings.Trim(revision, revisionHex) != "" {
		return "", providerkit.Refuse(providerkit.CodeDenied,
			"the records helper answered %q where a revision belongs, and a record nothing can compare is a record anything can lose", rendered)
	}
	return providerkit.Revision(revision), nil
}

func body(record providerkit.Record) []byte {
	return append([]byte(base64.StdEncoding.EncodeToString(record.Bytes)), '\n')
}

func readRow(rendered string) (providerkit.Revision, []byte, error) {
	revision, encoded, split := strings.Cut(strings.TrimRight(rendered, "\n"), "\t")
	if !split {
		return "", nil, providerkit.Refuse(providerkit.CodeDenied,
			"the records helper answered a read with %q, which is no record", rendered)
	}
	bytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, providerkit.Refuse(providerkit.CodeDenied,
			"the record read back is stored as no record ocel wrote")
	}
	return providerkit.Revision(revision), bytes, nil
}

func located(name providerkit.RecordName) (providerkit.Class, string, error) {
	class, named := providerkit.ClassOf(name)
	if !named {
		return "", "", providerkit.Refuse(providerkit.CodeInvalid,
			"%s names no class, and this host keeps one record tree per class", name)
	}
	encoded, err := encode(name)
	if err != nil {
		return "", "", err
	}
	return class, encoded, nil
}

func encode(name providerkit.RecordName) (string, error) {
	segments := make([]string, 0, len(name))
	for _, segment := range name {
		if segment == "" {
			return "", providerkit.Refuse(providerkit.CodeInvalid,
				"%s carries an empty segment, and no file on this host answers to it", name)
		}
		segments = append(segments, encodeSegment(segment))
	}
	return strings.Join(segments, "/"), nil
}

func encodeSegment(segment string) string {
	var written strings.Builder
	for i := 0; i < len(segment); i++ {
		if plain(segment[i]) && !(i == 0 && segment[i] == '.') {
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

func decode(encoded string) (providerkit.RecordName, error) {
	var name providerkit.RecordName
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
			return "", providerkit.Refuse(providerkit.CodeDenied,
				"the records helper named %q, which is not a name ocel wrote", segment)
		}
		value, err := strconv.ParseUint(segment[i+1:i+3], 16, 8)
		if err != nil {
			return "", providerkit.Refuse(providerkit.CodeDenied,
				"the records helper named %q, which is not a name ocel wrote", segment)
		}
		written.WriteByte(byte(value))
		i += 2
	}
	return written.String(), nil
}

var _ providerkit.RecordStore = (*Records)(nil)
