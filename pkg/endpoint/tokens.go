package endpoint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	connect "connectrpc.com/connect"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/ocelhq/ocel/pkg/environment"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	"github.com/ocelhq/ocel/pkg/scope"
)

const skew = 30 * time.Second

type ConsoleTokens struct {
	console        string
	audience       string
	consoleKeys    *Keyring
	credentials    *CredentialTrust
	credentialKeys *Keyring
}

func NewConsoleTokens(console, audience string, credentials *CredentialTrust) (*ConsoleTokens, error) {
	origin, err := originOf(console)
	if err != nil {
		return nil, err
	}
	if audience == "" {
		return nil, errors.New("endpoint: a console token names this endpoint as its audience, and none is set")
	}
	tokens := &ConsoleTokens{
		console:     origin,
		audience:    audience,
		consoleKeys: NewKeyring(origin + "/api/auth/jwks"),
	}
	if credentials != nil {
		if err := credentials.refuseIncomplete(); err != nil {
			return nil, fmt.Errorf("endpoint: %w", err)
		}
		tokens.credentials = credentials
		tokens.credentialKeys = NewKeyring(strings.TrimSuffix(credentials.Issuer, "/") + "/jwks")
	}
	return tokens, nil
}

func (t *ConsoleTokens) Verify(ctx context.Context, authorization string) (Caller, error) {
	raw, found := strings.CutPrefix(authorization, "Bearer ")
	if !found || raw == "" {
		return Caller{}, unauthenticated("no bearer token")
	}
	unverified, err := jwt.ParseInsecure([]byte(raw))
	if err != nil {
		return Caller{}, unauthenticated("the bearer token is not a jwt")
	}
	audiences, _ := unverified.Audience()
	if len(audiences) != 1 {
		return Caller{}, unauthenticated("the bearer token does not name exactly one audience")
	}
	switch audience := audiences[0]; {
	case audience == t.audience:
		token, err := t.parse(ctx, raw, t.consoleKeys, t.console, audience)
		if err != nil {
			return Caller{}, err
		}
		return t.userOf(token)
	case audience == JobAudience:
		token, err := t.parse(ctx, raw, t.consoleKeys, t.console, audience)
		if err != nil {
			return Caller{}, err
		}
		return t.jobOf(token)
	case t.credentials != nil && audience == t.credentials.Audience:
		token, err := t.parse(ctx, raw, t.credentialKeys, t.credentials.Issuer, audience)
		if err != nil {
			return Caller{}, err
		}
		return t.credentialOf(token)
	}
	return Caller{}, unauthenticated("the bearer token is for another audience")
}

func (t *ConsoleTokens) parse(ctx context.Context, raw string, keys *Keyring, issuer, audience string) (jwt.Token, error) {
	message, err := jws.ParseString(raw)
	if err != nil {
		return nil, unauthenticated("the bearer token is not a signed jwt")
	}
	signatures := message.Signatures()
	if len(signatures) != 1 {
		return nil, unauthenticated("the bearer token does not have exactly one signature")
	}
	kid, _ := signatures[0].ProtectedHeaders().KeyID()
	set, err := keys.Read(ctx, kid)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("reach the keys of %s: %w", issuer, err))
	}
	token, err := jwt.ParseString(raw,
		jwt.WithKeySet(set),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithAcceptableSkew(skew),
		jwt.WithRequiredClaim(jwt.ExpirationKey),
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("verify the bearer token: %w", err))
	}
	return token, nil
}

func (t *ConsoleTokens) userOf(token jwt.Token) (Caller, error) {
	subject, _ := token.Subject()
	organization, found := strings.CutPrefix(subject, "org:")
	if !found || organization == "" || strings.Contains(organization, ":") {
		return Caller{}, unauthenticated("the token names no organization")
	}
	var claimed any
	if err := token.Get("scope", &claimed); err != nil {
		return Caller{}, connect.NewError(connect.CodePermissionDenied, errors.New("the token has no scope"))
	}
	claims, ok := stringsOf(claimed)
	if !ok {
		return Caller{}, connect.NewError(connect.CodePermissionDenied, errors.New("the token's scope is not a list of names"))
	}
	var scopes []scopev1.Scope
	for _, claim := range claims {
		if held, named := scope.ScopeOfClaim(claim); named && scope.IsUserScope(held) {
			scopes = append(scopes, held)
		}
	}
	var actor, claimedRole string
	_ = token.Get("act", &actor)
	_ = token.Get("role", &claimedRole)
	role, _ := scope.RoleOfClaim(claimedRole)
	return NewUserCaller(t.console, organization, actor, role, scopes)
}

func (t *ConsoleTokens) jobOf(token jwt.Token) (Caller, error) {
	subject, _ := token.Subject()
	fields, err := subjectFields(subject, "org", "tier", "project")
	if err != nil {
		return Caller{}, err
	}
	tier, err := tierOf(fields["tier"])
	if err != nil {
		return Caller{}, err
	}
	job := Job{ProjectID: fields["project"], Tier: tier}
	for claim, into := range map[string]*string{"job_id": &job.ID, "runner": &job.Runner, "slug": &job.Slug} {
		if err := token.Get(claim, into); err != nil || *into == "" {
			return Caller{}, unauthenticated("the job token names no " + claim)
		}
	}
	_ = token.Get("deployment_id", &job.DeploymentID)
	_ = token.Get("preview", &job.Preview)
	if job.Preview != "" && job.Tier != environment.TierPreview {
		return Caller{}, forbidden("the job token names preview " + job.Preview + " in the " + string(job.Tier) + " tier")
	}
	job.TokenID, _ = token.JwtID()
	job.Expires, _ = token.Expiration()
	return NewJobCaller(t.console, fields["org"], job), nil
}

func (t *ConsoleTokens) credentialOf(token jwt.Token) (Caller, error) {
	var organization, target, rawTier, project, purpose string
	for claim, into := range map[string]*string{"org": &organization, "target": &target, "tier": &rawTier, "project": &project, "purpose": &purpose} {
		if err := token.Get(claim, into); err != nil || *into == "" {
			return Caller{}, unauthenticated("the credential names no " + claim)
		}
	}
	if target != t.credentials.TargetID {
		return Caller{}, forbidden(fmt.Sprintf("the credential is for target %q, not this one", target))
	}
	tier, err := tierOf(rawTier)
	if err != nil {
		return Caller{}, err
	}
	credential := TargetCredential{TargetID: target, ProjectID: project, Purpose: purpose, Tier: tier}
	credential.TokenID, _ = token.JwtID()
	credential.Expires, _ = token.Expiration()
	return NewCredentialCaller(t.console, organization, credential), nil
}

func subjectFields(subject string, names ...string) (map[string]string, error) {
	parts := strings.Split(subject, ":")
	if len(parts) != 2*len(names) {
		return nil, unauthenticated("the token's subject is not " + strings.Join(names, ":<>:") + ":<>")
	}
	fields := map[string]string{}
	for i, name := range names {
		if parts[2*i] != name || parts[2*i+1] == "" {
			return nil, unauthenticated("the token's subject is not " + strings.Join(names, ":<>:") + ":<>")
		}
		fields[name] = parts[2*i+1]
	}
	return fields, nil
}

func tierOf(raw string) (environment.Tier, error) {
	switch tier := environment.Tier(raw); tier {
	case environment.TierProduction, environment.TierPreview:
		return tier, nil
	}
	return "", forbidden(fmt.Sprintf("the token names the tier %q, which is neither production nor preview", raw))
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

func forbidden(message string) error {
	return connect.NewError(connect.CodePermissionDenied, errors.New(message))
}

func unauthenticated(message string) error {
	return connect.NewError(connect.CodeUnauthenticated, errors.New(message))
}
