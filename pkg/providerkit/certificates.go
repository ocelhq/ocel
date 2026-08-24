package providerkit

import (
	"context"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Certificate struct {
	ARN       string        `json:"arn,omitempty"`
	Region    string        `json:"region,omitempty"`
	Requested bool          `json:"requested,omitempty"`
	Written   []edge.Record `json:"written,omitempty"`
	Owed      []edge.Record `json:"owed,omitempty"`
}

func (c Certificate) Held() bool { return c.ARN != "" }

type Prover func(ctx context.Context, cert Certificate, records []edge.Record) (Certificate, error)

type CertificateRequest struct {
	Kind     edge.Kind
	Hostname string
	Held     Certificate
	Prove    Prover
	Notes    []string
	Report   Reporter
}

type Certifier interface {
	Certificate(ctx context.Context, req CertificateRequest) (Certificate, error)

	DiscardCertificate(ctx context.Context, cert Certificate, report Reporter) error
}

func certificateFor(ctx context.Context, provider Provider, req CertificateRequest) (Certificate, error) {
	certifier, ok := provider.(Certifier)
	if !ok {
		return Certificate{}, nil
	}
	return certifier.Certificate(ctx, req)
}

func discardCertificate(ctx context.Context, provider Provider, cert Certificate, report Reporter) error {
	certifier, ok := provider.(Certifier)
	if !ok || !cert.Requested || cert.ARN == "" {
		return nil
	}
	return certifier.DiscardCertificate(ctx, cert, report)
}

const proveNote = "Leave it in place: the certificate is renewed through it."

func proveHeadline(hostname string) string {
	return "Prove you own " + strings.TrimPrefix(hostname, "*.")
}
