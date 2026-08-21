package providerkit

import "context"

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
