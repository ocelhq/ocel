package envsource

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/envvars"
)

type Source interface {
	ID() string
	Read(ctx context.Context, folders []string) (map[envvars.Cell]Value, error)
	Create(ctx context.Context, at envvars.Cell, value []byte, description string) error
	URL(at envvars.Cell) string
}

type Value struct {
	Plaintext []byte
	Version   string
}

var (
	ErrExists = errors.New("envsource: the env source already has that key")

	ErrReadOnly = errors.New("envsource: ocel may not write into the env source")

	ErrAwaitingApproval = errors.New("envsource: the env source queued the write for approval")
)
