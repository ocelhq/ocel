package endpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"

	connect "connectrpc.com/connect"

	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	"github.com/ocelhq/ocel/pkg/scope"
)

type gate struct {
	verify            func(ctx context.Context, authorization string) (Caller, error)
	manyOrganizations bool
	organizationID    string
	endpointID        string
	allowed           []scopev1.Scope
	errors            *connect.ErrorWriter
	out               io.Writer
}

func (g *gate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		procedure := r.URL.Path
		caller, err := g.admit(r.Context(), procedure, r.Header.Get("Authorization"))
		if err != nil {
			g.say(caller, procedure, "denied")
			_ = g.errors.Write(w, r, err)
			return
		}
		g.say(caller, procedure, "pass")
		next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), caller)))
	})
}

func (g *gate) admit(ctx context.Context, procedure, authorization string) (Caller, error) {
	required, err := scope.RequiredScope(procedure)
	if err != nil {
		return Caller{}, connect.NewError(connect.CodePermissionDenied, err)
	}
	caller, err := g.verify(ctx, authorization)
	if err != nil {
		var wire *connect.Error
		if errors.As(err, &wire) {
			return Caller{}, err
		}
		return Caller{}, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if !g.manyOrganizations && caller.OrganizationID != g.organizationID {
		return caller, connect.NewError(connect.CodePermissionDenied, errors.New("the token speaks for another organization than this endpoint's"))
	}
	if !caller.Holds(required) {
		return caller, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("the token has no %s scope", scopeName(required)))
	}
	if scope.IsUserScope(required) && !slices.Contains(g.allowed, required) {
		return caller, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("this endpoint allows no %s calls: add %s to its allowedScopes", scopeName(required), scopeName(required)))
	}
	if job, ok := caller.Job(); ok && job.Runner != g.endpointID {
		return caller, connect.NewError(connect.CodePermissionDenied, errors.New("the job token names another runner"))
	}
	return caller, nil
}

func scopeName(named scopev1.Scope) string {
	if claim := scope.ClaimOf(named); claim != "" {
		return claim
	}
	return named.String()
}

func (g *gate) say(caller Caller, procedure, outcome string) {
	fmt.Fprintf(g.out, "act=%q org=%q procedure=%q outcome=%s\n",
		or(caller.Actor, "-"), or(caller.OrganizationID, "-"), procedure, outcome)
}

func or(named, fallback string) string {
	if named == "" {
		return fallback
	}
	return named
}
