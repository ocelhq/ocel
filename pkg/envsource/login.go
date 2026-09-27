package envsource

import (
	"context"
	"net/http"
)

type Login struct {
	ProveIdentity func(ctx context.Context, audience string) (IdentityProof, error)
	Client        *http.Client
}
