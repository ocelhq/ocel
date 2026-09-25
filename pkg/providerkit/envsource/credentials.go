package envsource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

type Target struct {
	Signer CallerIdentitySigner
	Issuer IDTokenIssuer
	Client *http.Client
}

type CredentialError struct {
	Var    string
	Reason string
}

func (e *CredentialError) Error() string { return e.Var + " " + e.Reason }

func Open(ctx context.Context, store values.Store, scope values.Scope, descriptor Descriptor, target Target) (Source, error) {
	credential, err := CredentialFor(ctx, store, scope, descriptor, target)
	if err != nil {
		return nil, err
	}
	return NewInfisical(*descriptor.Infisical, credential, target.Client), nil
}

func CredentialFor(ctx context.Context, store values.Store, scope values.Scope, descriptor Descriptor, target Target) (Credential, error) {
	if descriptor.Kind != Infisical || descriptor.Infisical == nil {
		return nil, fmt.Errorf("a %s source is read where ocel runs, never by the target", descriptor.Kind)
	}
	auth := descriptor.Infisical.Auth
	switch auth.Method {
	case AuthAWS:
		return AWSAuth(auth.IdentityID, target.Signer), nil
	case AuthGCP:
		return GCPAuth(auth.IdentityID, target.Issuer), nil
	case AuthUniversal:
		landed := make([]values.Landing, 0, 2)
		for _, name := range auth.Vars() {
			landing, err := landCredential(ctx, store, scope, name)
			if err != nil {
				return nil, err
			}
			landed = append(landed, landing)
		}
		return UniversalAuth(landed[0].Plaintext, landed[1].Plaintext), nil
	}
	return nil, errors.New("this Infisical source names no identity to sign in as")
}

func landCredential(ctx context.Context, store values.Store, scope values.Scope, name string) (values.Landing, error) {
	landing, err := store.Land(ctx, scope, values.Coordinate{Cell: values.Cell{Key: name}}, true)
	switch {
	case errors.Is(err, values.ErrNotFound), errors.Is(err, values.ErrDangling):
		return values.Landing{}, &CredentialError{Var: name, Reason: fmt.Sprintf("holds no value in %s: set it with `ocel env set %s=<VALUE>%s`", scope.Class, name, previewFlag(scope))}
	case err != nil:
		return values.Landing{}, fmt.Errorf("read %s: %w", name, err)
	}
	if owner := landing.Provenance.EnvSource; owner != "" {
		return values.Landing{}, &CredentialError{Var: name, Reason: fmt.Sprintf("reads a value %s wrote, and a source cannot hold the credential it is read with: set %s in ocel's own store", owner, name)}
	}
	if landing.Project == scope.Project {
		return landing, nil
	}
	registration, registered, err := Registered(ctx, store.Records, scope.Class, landing.Project)
	if err != nil {
		return values.Landing{}, err
	}
	if registered && !slices.Contains(registration.Credentials(), landing.Coordinate.Cell) {
		return values.Landing{}, &CredentialError{Var: name, Reason: fmt.Sprintf("references %s in %s, which reads that value from its own env source: reference a value %s holds in ocel's own store", landing.Coordinate.Key, landing.Project, landing.Project)}
	}
	return landing, nil
}

func previewFlag(scope values.Scope) string {
	if scope.Class == ports.ClassPreview {
		return " --preview"
	}
	return ""
}

func Identity(ctx context.Context, store values.Store, scope values.Scope, descriptor Descriptor) string {
	options := descriptor.Infisical
	if options == nil {
		return string(descriptor.Kind) + ":" + scope.Project
	}
	parts := []string{string(descriptor.Kind), options.Host, options.Project, options.Environment, options.Path, string(options.Auth.Method)}
	switch options.Auth.Method {
	case AuthAWS, AuthGCP:
		parts = append(parts, options.Auth.IdentityID)
	case AuthUniversal:
		for _, name := range options.Auth.Vars() {
			landing, err := store.Land(ctx, scope, values.Coordinate{Cell: values.Cell{Key: name}}, false)
			if err != nil {
				parts = append(parts, "unset", scope.Project, name)
				continue
			}
			parts = append(parts, landing.Project, landing.Coordinate.Folder, landing.Coordinate.Key)
		}
	}
	return strings.Join(parts, "\x00")
}
