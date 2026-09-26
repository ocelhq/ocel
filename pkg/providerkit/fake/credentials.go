package fake

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Credentials struct {
	mu      sync.Mutex
	region  string
	refusal error
}

func NewCredentials(region string) *Credentials { return &Credentials{region: region} }

func (c *Credentials) Deny(hint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refusal = refusal.Refuse(refusal.CodeDenied, "%s", hint)
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

func (c *Credentials) Permissions(tier edge.CredentialTier) (edge.CredentialDocument, error) {
	return edge.CredentialDocument{
		Heading:  "fake credentials",
		Document: "fake permissions for " + string(tier),
	}, nil
}
