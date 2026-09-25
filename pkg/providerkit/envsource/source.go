package envsource

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

type Caps struct {
	Read     bool
	List     bool
	Write    bool
	Standing bool
}

type Resolved struct {
	Value   []byte
	Version string
}

type Source interface {
	ID() string
	Capabilities() Caps
	Resolve(ctx context.Context, folders []string) (map[values.Cell]Resolved, error)
	Put(ctx context.Context, at values.Cell, value []byte, description string) error
	Link(at values.Cell) string
}

var (
	ErrExists = errors.New("envsource: the source already holds that key")

	ErrReadOnly = errors.New("envsource: the source is read-only to ocel")

	ErrAwaitingApproval = errors.New("envsource: the source holds the write for approval")
)
