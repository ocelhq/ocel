package records

import (
	"context"
	"errors"
	"fmt"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Store interface {
	Read(ctx context.Context, name Name) (Record, error)

	Write(ctx context.Context, record Record) (Revision, error)

	WritePair(ctx context.Context, first, second Record) error

	Remove(ctx context.Context, name Name, expected Revision) error

	List(ctx context.Context, under Name) ([]Record, error)
}

var ErrStale = errors.New("the record moved since it was read")

var ErrNotFound = errors.New("no such record")

const (
	RootSchema       = "schema"
	RootProjects     = "projects"
	RootStacks       = "stacks"
	RootEnvironments = "environments"
	RootBootstrap    = "bootstrap"
	RootEdgeStacks   = "edgestacks"
	RootWildcard     = "wildcard"
	RootLedger       = "ledger"
	RootValues       = "values"
	RootValueRefs    = "valuerefs"
	RootConformance  = "conformance"
)

type Name []string

func (n Name) String() string { return strings.Join(n, "/") }

func (n Name) Under(prefix Name) (Name, bool) {
	if len(n) <= len(prefix) {
		return nil, false
	}
	for i, segment := range prefix {
		if n[i] != segment {
			return nil, false
		}
	}
	return n[len(prefix):], true
}

type Revision string

type Record struct {
	Name     Name
	Bytes    []byte
	Revision Revision
}

func ReadOrEmpty(ctx context.Context, store Store, name Name) (Record, error) {
	held, err := store.Read(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return Record{Name: name}, nil
	}
	if err != nil {
		return Record{}, err
	}
	held.Name = name
	return held, nil
}

func Forget(ctx context.Context, store Store, name Name) error {
	for range forgetAttempts {
		held, err := store.Read(ctx, name)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		err = store.Remove(ctx, name, held.Revision)
		if err == nil || errors.Is(err, ErrNotFound) {
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

type Cipher interface {
	Seal(ctx context.Context, at SealScope, plaintext []byte) ([]byte, error)

	Open(ctx context.Context, at SealScope, sealed []byte) ([]byte, error)
}

type SealScope struct {
	Project string
	Class   edge.Class
	Env     string
	Folder  string
	Binding string
	Name    string
}
