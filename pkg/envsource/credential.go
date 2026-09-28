package envsource

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envvars"
)

type Credential interface {
	logIn(ctx context.Context, client *infisicalClient) (session, error)
	sameAs(other Credential) bool
}

type CredentialError struct {
	Variable string
	Unset    bool
	Reason   string
}

func (e *CredentialError) Error() string { return e.Variable + " " + e.Reason }

func ReadCredential(ctx context.Context, store envvars.Store, scope envvars.Scope, descriptor Descriptor, login Login) (Credential, error) {
	if descriptor.Kind != Infisical || descriptor.Infisical == nil {
		return nil, fmt.Errorf("a %s env source is read where ocel runs, never with a credential a target stores", descriptor.Kind)
	}
	auth := descriptor.Infisical.Auth
	switch auth.Method {
	case AuthIdentity:
		return IdentityAuth(auth.IdentityID, login.ProveIdentity), nil
	case AuthUniversal:
		plaintexts := make([]string, 0, 2)
		var refused []error
		for _, name := range auth.Variables() {
			plaintext, err := readCredentialValue(ctx, store, scope, name)
			var credential *CredentialError
			switch {
			case errors.As(err, &credential):
				refused = append(refused, err)
			case err != nil:
				return nil, err
			}
			plaintexts = append(plaintexts, plaintext)
		}
		if len(refused) > 0 {
			return nil, errors.Join(refused...)
		}
		return UniversalAuth(plaintexts[0], plaintexts[1]), nil
	}
	return nil, fmt.Errorf("%s names no identity to log in to Infisical as", descriptor.ID())
}

func readCredentialValue(ctx context.Context, store envvars.Store, scope envvars.Scope, name string) (string, error) {
	found, err := store.GetDereferenced(ctx, scope, envvars.Coordinate{Cell: envvars.Cell{Key: name}}, true)
	switch {
	case errors.Is(err, envvars.ErrNotFound), errors.Is(err, envvars.ErrDangling):
		return "", &CredentialError{Variable: name, Unset: true, Reason: fmt.Sprintf("has no value in %s: set it with `%s`", scope.Tier, SetCommand(scope.Tier, name))}
	case err != nil:
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	if wrote := found.Provenance.EnvSource; wrote != "" {
		return "", &CredentialError{Variable: name, Reason: fmt.Sprintf("reads a value %s wrote, and an env source cannot store the credential it is read with: set %s in ocel's own store", wrote, name)}
	}
	if found.Project == scope.Project {
		return found.Plaintext, nil
	}
	registration, registered, err := Registered(ctx, store.KeyValues, scope.Tier, found.Project)
	if err != nil {
		return "", err
	}
	if registered && !slices.Contains(registration.Credentials(), found.Coordinate.Cell) {
		return "", &CredentialError{Variable: name, Reason: fmt.Sprintf("references %s in %s, which reads that value from its own env source: reference a value %s stores in ocel's own store", found.Coordinate.Key, found.Project, found.Project)}
	}
	return found.Plaintext, nil
}

func SetCommand(tier environment.Tier, name string) string {
	command := "ocel env set " + name + "=<VALUE>"
	if tier == environment.TierPreview {
		command += " --preview"
	}
	return command
}

type session struct {
	token     string
	expiresAt time.Time
	fixed     bool
}

type SignedRequest struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

type universalAuth struct{ clientID, clientSecret string }

func UniversalAuth(clientID, clientSecret string) Credential {
	return universalAuth{clientID: clientID, clientSecret: clientSecret}
}

func (u universalAuth) sameAs(other Credential) bool {
	same, ok := other.(universalAuth)
	return ok && same == u
}

func (u universalAuth) logIn(ctx context.Context, client *infisicalClient) (session, error) {
	return client.logIn(ctx, "universal-auth", map[string]string{"clientId": u.clientID, "clientSecret": u.clientSecret})
}

type IdentityProof struct {
	SignedRequest *SignedRequest
	IDToken       string
}

type identityAuth struct {
	identityID string
	prove      func(ctx context.Context, audience string) (IdentityProof, error)
}

func IdentityAuth(identityID string, proveIdentity func(ctx context.Context, audience string) (IdentityProof, error)) Credential {
	return identityAuth{identityID: identityID, prove: proveIdentity}
}

func (a identityAuth) sameAs(other Credential) bool {
	same, ok := other.(identityAuth)
	return ok && same.identityID == a.identityID
}

func (a identityAuth) logIn(ctx context.Context, client *infisicalClient) (session, error) {
	if a.prove == nil {
		return session{}, errors.New("identity auth logs in to Infisical as this target's own cloud identity, and this target has none")
	}
	proof, err := a.prove(ctx, a.identityID)
	if err != nil {
		return session{}, fmt.Errorf("prove this target's cloud identity to Infisical: %w", err)
	}
	switch {
	case proof.SignedRequest != nil && proof.IDToken == "":
		return logInWithSignedRequest(ctx, client, a.identityID, *proof.SignedRequest)
	case proof.IDToken != "" && proof.SignedRequest == nil:
		return client.logIn(ctx, "gcp-auth", map[string]string{"identityId": a.identityID, "jwt": proof.IDToken})
	}
	return session{}, errors.New("this target's cloud identity proved itself with neither one signed request nor one ID token, and Infisical accepts nothing else")
}

func logInWithSignedRequest(ctx context.Context, client *infisicalClient, identityID string, signed SignedRequest) (session, error) {
	endpoint, err := url.Parse(signed.URL)
	if err != nil {
		return session{}, fmt.Errorf("the signed request names no endpoint: %w", err)
	}
	headers := map[string]string{}
	for name, values := range signed.Header {
		if len(values) > 0 && !strings.EqualFold(name, "Content-Length") {
			headers[http.CanonicalHeaderKey(name)] = values[0]
		}
	}
	headers["Host"] = endpoint.Host
	headers["Content-Type"] = "application/x-www-form-urlencoded; charset=utf-8"
	headers["Content-Length"] = strconv.Itoa(len(signed.Body))
	encoded, err := json.Marshal(headers)
	if err != nil {
		return session{}, err
	}
	return client.logIn(ctx, "aws-auth", map[string]string{
		"identityId":           identityID,
		"iamHttpRequestMethod": signed.Method,
		"iamRequestBody":       base64.StdEncoding.EncodeToString(signed.Body),
		"iamRequestHeaders":    base64.StdEncoding.EncodeToString(encoded),
	})
}

type accessToken string

func AccessToken(token string) Credential { return accessToken(token) }

func (a accessToken) sameAs(other Credential) bool {
	same, ok := other.(accessToken)
	return ok && same == a
}

func (a accessToken) logIn(context.Context, *infisicalClient) (session, error) {
	return session{token: string(a), fixed: true}, nil
}
