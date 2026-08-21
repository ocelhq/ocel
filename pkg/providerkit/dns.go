package providerkit

import (
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

// DNSRegistry is which zone APIs this provider can write records through. It is
// a third axis, not a branch of the other two: the AWS provider already writes
// records through Route 53 *or* through Cloudflare, and which one is a per-project
// choice on the wire rather than a consequence of the origin or the edge.
//
// The kit owns everything around it — the settle loop, the certificate dance, the
// resolver poll, and the records-owed prompt when there is no writer at all.
type DNSRegistry interface {
	Supported() []DNSKind

	// Open builds a writer for a zone. A zero kind is the case where the user
	// keeps their own DNS: there is no writer, the kit prints the records owed
	// and waits for them to appear, so a nil writer and a nil error is a correct
	// and common answer.
	Open(kind DNSKind, zone string) (edge.DNSWriter, error)
}

// DNSKind names a zone API — "route53", "cloudflare". Free vocabulary the CLI
// prints and matches against what Supported returned; the kit branches on none of
// it.
type DNSKind string

// Resolving is not a port. Waiting for a record to appear is one call to a stub
// resolver and a timer, identical on every vendor, so the kit does it. A vendor
// that made this its own would only get it wrong differently.

// Certificates are not a port either. They belong to whoever terminates TLS,
// which is the edge — CloudFront wants ACM in us-east-1, Cloudflare issues its
// own — so the kit asks the edge, not the origin.
//
// The precedent already exists as certs.OriginCertifier in the AWS provider: an
// assertion on edge.Edge that answers which region a certificate must live in.
// Lifting it needs #390 to put a certificate surface on the edge contract, which
// this kit then composes as it stands.
type Certifier interface {
	// Certify issues or finds a certificate covering these hostnames, and reports
	// what the user still owes in DNS to validate it.
	Certify(hostnames []string) (edge.Record, error)
}
