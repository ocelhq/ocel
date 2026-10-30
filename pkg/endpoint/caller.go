package endpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	"github.com/ocelhq/ocel/pkg/scope"
)

type CallerKind int

const (
	KindUser CallerKind = iota + 1
	KindJob
	KindCredential
)

type Caller struct {
	Console        string
	OrganizationID string
	Actor          string

	kind       CallerKind
	role       scopev1.Role
	userScopes []scopev1.Scope
	job        Job
	credential TargetCredential
}

func NewUserCaller(console, organizationID, actor string, role scopev1.Role, scopes []scopev1.Scope) (Caller, error) {
	for _, held := range scopes {
		if !scope.IsUserScope(held) {
			return Caller{}, fmt.Errorf("%s is no scope a person holds", held)
		}
	}
	return Caller{Console: console, OrganizationID: organizationID, Actor: actor, kind: KindUser, role: role, userScopes: slices.Clone(scopes)}, nil
}

func NewJobCaller(console, organizationID string, job Job) Caller {
	return Caller{Console: console, OrganizationID: organizationID, Actor: "job:" + job.ID, kind: KindJob, job: job}
}

func NewCredentialCaller(console, organizationID string, credential TargetCredential) Caller {
	return Caller{Console: console, OrganizationID: organizationID, Actor: "credential:" + credential.TokenID, kind: KindCredential, credential: credential}
}

func (c Caller) Kind() CallerKind { return c.kind }

func (c Caller) Scopes() []scopev1.Scope {
	switch c.kind {
	case KindUser:
		return slices.Clone(c.userScopes)
	case KindJob:
		return []scopev1.Scope{scopev1.Scope_SCOPE_JOB}
	case KindCredential:
		return []scopev1.Scope{scopev1.Scope_SCOPE_TARGET_CREDENTIAL}
	}
	return nil
}

func (c Caller) HoldsRole(least scopev1.Role) bool {
	return c.kind == KindUser && least != scopev1.Role_ROLE_UNSPECIFIED && c.role >= least
}

func (c Caller) Job() (Job, bool) { return c.job, c.kind == KindJob }

func (c Caller) Credential() (TargetCredential, bool) {
	return c.credential, c.kind == KindCredential
}

type Job struct {
	ID           string
	ProjectID    string
	DeploymentID string
	Runner       string
	Slug         string
	Preview      string
	TokenID      string
	Tier         environment.Tier
	Expires      time.Time
}

type TargetCredential struct {
	TargetID  string
	ProjectID string
	Purpose   string
	TokenID   string
	Tier      environment.Tier
	Expires   time.Time
}

func (c Caller) Tenant() string {
	sum := sha256.Sum256([]byte(c.Console + "\x00" + c.OrganizationID))
	return hex.EncodeToString(sum[:12])
}

func (c Caller) Holds(required scopev1.Scope) bool {
	return required != scopev1.Scope_SCOPE_UNSPECIFIED && slices.Contains(c.Scopes(), required)
}

type callerKey struct{}

func WithCaller(ctx context.Context, caller Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, caller)
}

func CallerOf(ctx context.Context) (Caller, bool) {
	caller, ok := ctx.Value(callerKey{}).(Caller)
	return caller, ok
}
