package ports

import (
	"context"
	"errors"
	"fmt"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type RecordStore interface {
	Read(ctx context.Context, name RecordName) (Record, error)

	Write(ctx context.Context, record Record) (Revision, error)

	WritePair(ctx context.Context, first, second Record) error

	Remove(ctx context.Context, name RecordName, expected Revision) error

	List(ctx context.Context, under RecordName) ([]Record, error)
}

var ErrStale = errors.New("the record moved since it was read")

var ErrNoRecord = errors.New("no such record")

type RecordName []string

func (n RecordName) String() string { return strings.Join(n, "/") }

type Revision string

type Record struct {
	Name     RecordName
	Bytes    []byte
	Revision Revision
}

func Held(ctx context.Context, records RecordStore, name RecordName) (Record, error) {
	held, err := records.Read(ctx, name)
	if errors.Is(err, ErrNoRecord) {
		return Record{Name: name}, nil
	}
	if err != nil {
		return Record{}, err
	}
	held.Name = name
	return held, nil
}

func Forget(ctx context.Context, records RecordStore, name RecordName) error {
	for range forgetAttempts {
		held, err := records.Read(ctx, name)
		if errors.Is(err, ErrNoRecord) {
			return nil
		}
		if err != nil {
			return err
		}
		err = records.Remove(ctx, name, held.Revision)
		if err == nil || errors.Is(err, ErrNoRecord) {
			return nil
		}
		if !errors.Is(err, ErrStale) {
			return err
		}
	}
	return fmt.Errorf(
		"%s was rewritten between every read of it and the removal that followed, %d times over. "+
			"Something is still writing that record; removing it now would drop a write nobody has seen",
		name, forgetAttempts)
}

const forgetAttempts = 5

type Sealer interface {
	Seal(ctx context.Context, at Coordinate, plaintext []byte) ([]byte, error)

	Open(ctx context.Context, at Coordinate, sealed []byte) ([]byte, error)
}

type Coordinate struct {
	Project string
	Class   Class
	Env     string
	Folder  string
	Link    string
	Name    string
}

type Class = edge.Class

const (
	ClassProduction = edge.ClassProduction
	ClassPreview    = edge.ClassPreview
)

type Code string

const (
	CodeInvalid  Code = "invalid"
	CodeNotReady Code = "not-ready"
	CodeDenied   Code = "denied"
	CodeBusy     Code = "busy"
)

type Refusal struct {
	Code    Code
	Message string
}

func (r Refusal) Error() string { return r.Message }

func Refuse(code Code, format string, args ...any) error {
	return Refusal{Code: code, Message: fmt.Sprintf(format, args...)}
}
