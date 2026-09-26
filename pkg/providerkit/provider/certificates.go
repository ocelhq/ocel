package provider

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Certificate struct {
	ID        string        `json:"id,omitempty"`
	Requested bool          `json:"requested,omitempty"`
	Written   []edge.Record `json:"written,omitempty"`
	Owed      []edge.Record `json:"owed,omitempty"`
}

func (c Certificate) Issued() bool { return c.ID != "" }

type CertificateRequest struct {
	Kind     edge.Kind
	Hostname string
	Current  Certificate
	Prove    func(ctx context.Context, cert Certificate, records []edge.Record) (Certificate, error)
	Progress edge.Progress
}

type CertificateHealth struct {
	Terminates   bool
	Status       string
	Issued       bool
	Domains      []string
	Covers       bool
	Renewal      string
	ExpiresAt    int64
	ExpiringSoon bool
}

type Certificates interface {
	Issue(ctx context.Context, req CertificateRequest) (Certificate, error)

	Inspect(ctx context.Context, kind edge.Kind, hostname string, cert Certificate) (CertificateHealth, error)

	Discard(ctx context.Context, cert Certificate, progress edge.Progress) error
}
