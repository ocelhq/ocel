package fake

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type Credentials struct {
	mu          sync.Mutex
	region      string
	refusal     error
	permissions *edge.CredentialDocument
}

func NewCredentials(region string) *Credentials { return &Credentials{region: region} }

func (c *Credentials) Deny(hint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refusal = refusal.Refuse(refusal.CodeDenied, "%s", hint)
}

func (c *Credentials) Ask(message string, question provider.Question) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refusal = provider.Ask(message, question)
}

func (c *Credentials) Admit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refusal = nil
}

func (c *Credentials) Whoami(context.Context) (provider.Principal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refusal != nil {
		return provider.Principal{}, c.refusal
	}
	return provider.Principal{
		Vendor:  Vendor,
		Account: "000000000000",
		Name:    "fake/reference",
		Details: []provider.PrincipalDetail{{Label: "region", Value: c.region}},
	}, nil
}

func (c *Credentials) DocumentsPermissions(document edge.CredentialDocument) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.permissions = &document
}

func (c *Credentials) Permissions(purpose edge.CredentialPurpose, tier environment.Tier) (edge.CredentialDocument, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.permissions != nil {
		return *c.permissions, nil
	}
	return edge.CredentialDocument{
		Heading:  "fake credentials",
		Document: "fake permissions for " + string(purpose) + " in " + string(tier),
	}, nil
}
