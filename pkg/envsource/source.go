package envsource

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/variablestore"
)

type Source interface {
	ID() string
	Read(ctx context.Context, folders []string) (map[variablestore.Cell]Value, error)
	Create(ctx context.Context, at variablestore.Cell, value []byte, description string) error
	Update(ctx context.Context, at variablestore.Cell, value []byte, copiedVersion string) error
	URL(at variablestore.Cell) string
}

type Value struct {
	Plaintext []byte
	Version   string
}

var (
	ErrExists = errors.New("envsource: the env source already has that key")

	ErrReadOnly = errors.New("envsource: ocel may not write into the env source")

	ErrAwaitingApproval = errors.New("envsource: the env source queued the write for approval")

	ErrChangedSinceRead = errors.New("envsource: the value changed in the env source since ocel last read it")

	ErrNotInFolder = errors.New("envsource: the env source keeps no such key in that folder itself")
)
