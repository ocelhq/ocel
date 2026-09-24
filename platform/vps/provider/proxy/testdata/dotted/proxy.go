package dotted

import (
	"context"

	. "github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ Proxy = Dotted{}

func (Dotted) Guarantees() Guarantees { return Guarantees{} }

func (Dotted) Admit(context.Context, Admission) error { return nil }

func (Dotted) Inspect(context.Context) (Standing, error) { return nil, nil }

func (Dotted) Certificate(context.Context, string) (Certificate, error) {
	return Certificate{}, nil
}

func (Dotted) Forget(context.Context, []string) ([]string, error) { return nil, nil }
