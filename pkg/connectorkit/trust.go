package connectorkit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	connect "connectrpc.com/connect"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
)

const skew = 30 * time.Second

const refetchFloor = 5 * time.Second

type keyring struct {
	url string

	mu        sync.Mutex
	set       jwk.Set
	fetchedAt time.Time
}

func (k *keyring) forKeyID(ctx context.Context, kid string) (jwk.Set, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.set != nil {
		if _, found := k.set.LookupKeyID(kid); found {
			return k.set, nil
		}
		if time.Since(k.fetchedAt) < refetchFloor {
			return k.set, nil
		}
	}

	fetched, err := jwk.Fetch(ctx, k.url)
	if err != nil {
		return nil, err
	}
	k.set = fetched
	k.fetchedAt = time.Now()
	return fetched, nil
}

type trust struct {
	issuer      string
	connectorID string
	subject     string
	grants      []string
	keys        *keyring
}

func newTrust(spec Spec) (*trust, error) {
	parsed, err := url.Parse(spec.Console)
	if err != nil {
		return nil, fmt.Errorf("parse console url %q: %w", spec.Console, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("console url %q names no origin", spec.Console)
	}
	origin := parsed.Scheme + "://" + parsed.Host
	return &trust{
		issuer:      origin,
		connectorID: spec.ConnectorID,
		subject:     "org:" + spec.OrganizationID,
		grants:      spec.Grants,
		keys:        &keyring{url: origin + "/api/auth/jwks"},
	}, nil
}

func (t *trust) interceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			held, err := t.scopes(ctx, request.Header().Get("Authorization"))
			if err != nil {
				return nil, err
			}
			want := scopeOf(request.Spec().Procedure, request.Any())
			if !slices.Contains(t.grants, want) {
				return nil, connect.NewError(connect.CodePermissionDenied,
					fmt.Errorf("this connector holds no %s grant", want))
			}
			if !slices.Contains(held, want) {
				return nil, connect.NewError(connect.CodePermissionDenied,
					fmt.Errorf("the token carries no %s scope", want))
			}
			return next(ctx, request)
		}
	})
}

func (t *trust) scopes(ctx context.Context, authorization string) ([]string, error) {
	raw, found := strings.CutPrefix(authorization, "Bearer ")
	if !found || raw == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no bearer token"))
	}

	message, err := jws.ParseString(raw)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("the bearer token is not a signed jwt"))
	}
	signatures := message.Signatures()
	if len(signatures) != 1 {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("the bearer token carries no single signature"))
	}
	kid, _ := signatures[0].ProtectedHeaders().KeyID()

	set, err := t.keys.forKeyID(ctx, kid)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("reach the console keys: %w", err))
	}

	token, err := jwt.ParseString(raw,
		jwt.WithKeySet(set),
		jwt.WithIssuer(t.issuer),
		jwt.WithAudience(t.connectorID),
		jwt.WithAcceptableSkew(skew),
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("verify the bearer token: %w", err))
	}

	subject, _ := token.Subject()
	if subject != t.subject {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("the token speaks for %q, not for this account", subject))
	}

	var claimed any
	if err := token.Get("scope", &claimed); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("the token carries no scope"))
	}
	held, ok := stringsOf(claimed)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("the token's scope is not a list of names"))
	}
	return held, nil
}

func stringsOf(claimed any) ([]string, bool) {
	switch value := claimed.(type) {
	case []string:
		return value, true
	case []any:
		out := make([]string, 0, len(value))
		for _, entry := range value {
			name, ok := entry.(string)
			if !ok {
				return nil, false
			}
			out = append(out, name)
		}
		return out, true
	}
	return nil, false
}

func scopeOf(procedure string, message any) string {
	switch procedure {
	case envvarsv1connect.EnvVarsServiceSetValueProcedure,
		envvarsv1connect.EnvVarsServiceDeleteValueProcedure,
		envvarsv1connect.EnvVarsServiceSetReferenceProcedure,
		envvarsv1connect.EnvVarsServiceSetBindingProcedure,
		envvarsv1connect.EnvVarsServiceRemoveBindingProcedure:
		return CapabilityEnvVarsWrite
	case envvarsv1connect.EnvVarsServiceRevealValuesProcedure:
		return CapabilityEnvVarsReveal
	case envvarsv1connect.EnvVarsServiceGetValueProcedure:
		if asked, ok := message.(*envvarsv1.GetValueRequest); ok && asked.GetReveal() {
			return CapabilityEnvVarsReveal
		}
		return CapabilityEnvVarsRead
	}
	return CapabilityEnvVarsRead
}
