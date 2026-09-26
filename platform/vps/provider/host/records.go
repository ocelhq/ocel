package host

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"io"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

//go:embed records.sh
var recordsScript []byte

const (
	ExitNoRecord = 3
	ExitStale    = 4
)

const (
	revisionWidth = 32
	revisionHex   = "0123456789abcdef"
	acknowledged  = "removed"
)

type RecordTransport interface {
	Holds(ctx context.Context, class edge.Class) (bool, error)

	Records(ctx context.Context, class edge.Class, stdin io.Reader, argv ...string) (string, error)
}

type Records struct{ over RecordTransport }

func NewRecords(h *Host) *Records { return &Records{over: sshRecords{host: h}} }

func RecordsOver(over RecordTransport) *Records { return &Records{over: over} }

func (r *Records) tier(ctx context.Context, name records.Name) (edge.Class, string, bool, error) {
	class, encoded, err := live.Located(name)
	if err != nil {
		return "", "", false, err
	}
	stood, err := r.over.Holds(ctx, class)
	if err != nil {
		return "", "", false, err
	}
	return class, encoded, stood, nil
}

func unbootstrapped(class edge.Class) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"this host has no ocel bootstrap\nRun `%s`",
		providerkit.BootstrapCommand(class))
}

func (r *Records) Read(ctx context.Context, name records.Name) (records.Record, error) {
	class, encoded, stood, err := r.tier(ctx, name)
	if err != nil {
		return records.Record{}, err
	}
	if !stood {
		return records.Record{}, records.ErrNotFound
	}
	rendered, err := r.helper(ctx, class, nil, "read", encoded)
	if err != nil {
		return records.Record{}, err
	}
	revision, body, err := readRow(rendered)
	if err != nil {
		return records.Record{}, err
	}
	return records.Record{Name: name, Bytes: body, Revision: revision}, nil
}

func (r *Records) Write(ctx context.Context, record records.Record) (records.Revision, error) {
	class, encoded, stood, err := r.tier(ctx, record.Name)
	if err != nil {
		return "", err
	}
	if !stood {
		return "", unbootstrapped(class)
	}
	rendered, err := r.helper(ctx, class, bytes.NewReader(body(record)), "write", encoded, string(record.Revision))
	if err != nil {
		return "", err
	}
	return minted(rendered)
}

func (r *Records) WritePair(ctx context.Context, first, second records.Record) error {
	class, one, stood, err := r.tier(ctx, first.Name)
	if err != nil {
		return err
	}
	beside, two, err := live.Located(second.Name)
	if err != nil {
		return err
	}
	if beside != class {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s and %s belong to different classes",
			first.Name, second.Name)
	}
	if !stood {
		return unbootstrapped(class)
	}
	fed := append(body(first), body(second)...)
	rendered, err := r.helper(ctx, class, bytes.NewReader(fed), "pair", one, string(first.Revision), two, string(second.Revision))
	if err != nil {
		return err
	}
	left, right, split := strings.Cut(strings.TrimSpace(rendered), "\t")
	if !split {
		return refusal.Refuse(refusal.CodeDenied,
			"the records helper answered a pair with %q", rendered)
	}
	if _, err := minted(left); err != nil {
		return err
	}
	_, err = minted(right)
	return err
}

func (r *Records) Remove(ctx context.Context, name records.Name, expected records.Revision) error {
	class, encoded, stood, err := r.tier(ctx, name)
	if err != nil {
		return err
	}
	if !stood {
		return records.ErrNotFound
	}
	rendered, err := r.helper(ctx, class, nil, "remove", encoded, string(expected))
	if err != nil {
		return err
	}
	if strings.TrimSpace(rendered) != acknowledged {
		return refusal.Refuse(refusal.CodeDenied,
			"the records helper did not confirm removing %s", name)
	}
	return nil
}

func (r *Records) List(ctx context.Context, under records.Name) ([]records.Record, error) {
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
	var held []records.Record
	for _, line := range strings.Split(strings.TrimSpace(rendered), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		columns := strings.SplitN(line, "\t", 3)
		if len(columns) != 3 {
			return nil, refusal.Refuse(refusal.CodeDenied,
				"the records helper listed a row ocel cannot read: %q", line)
		}
		name, err := live.DecodeName(columns[0])
		if err != nil {
			return nil, err
		}
		bytes, err := base64.StdEncoding.DecodeString(columns[2])
		if err != nil {
			return nil, refusal.Refuse(refusal.CodeDenied, "%s is not a record ocel wrote", name)
		}
		held = append(held, records.Record{Name: name, Bytes: bytes, Revision: records.Revision(columns[1])})
	}
	return held, nil
}

func (r *Records) helper(ctx context.Context, class edge.Class, stdin io.Reader, args ...string) (string, error) {
	return r.over.Records(ctx, class, stdin, args...)
}

type sshRecords struct{ host *Host }

func (s sshRecords) Holds(ctx context.Context, class edge.Class) (bool, error) {
	return s.host.holds(ctx, class)
}

func (s sshRecords) Records(ctx context.Context, class edge.Class, stdin io.Reader, argv ...string) (string, error) {
	command := quoted(recordsHelper) + " " + quoted(string(class))
	for _, arg := range argv {
		command += " " + quoted(arg)
	}
	elevation, refused := s.host.elevate(ctx)
	result, err := s.host.stream(ctx, command, stdin, elevation)
	if err != nil {
		return "", err
	}
	switch result.Code {
	case 0:
		return result.Stdout, nil
	case ExitNoRecord:
		return "", records.ErrNotFound
	case ExitStale:
		return "", records.ErrStale
	default:
		return "", unelevated(refused, s.host.refuse("records "+argv[0], result, elevation))
	}
}

const RecordsHelper = recordsHelper

func minted(rendered string) (records.Revision, error) {
	revision := strings.TrimSpace(rendered)
	if len(revision) != revisionWidth || strings.Trim(revision, revisionHex) != "" {
		return "", refusal.Refuse(refusal.CodeDenied,
			"the records helper answered %q, not a revision", rendered)
	}
	return records.Revision(revision), nil
}

func body(record records.Record) []byte {
	return append([]byte(base64.StdEncoding.EncodeToString(record.Bytes)), '\n')
}

func readRow(rendered string) (records.Revision, []byte, error) {
	revision, encoded, split := strings.Cut(strings.TrimRight(rendered, "\n"), "\t")
	if !split {
		return "", nil, refusal.Refuse(refusal.CodeDenied,
			"the records helper answered a read with %q", rendered)
	}
	bytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, refusal.Refuse(refusal.CodeDenied,
			"the record read back is not one ocel wrote")
	}
	return records.Revision(revision), bytes, nil
}

var _ records.Store = (*Records)(nil)
