// Package providerkit is the Go shape behind the generic provider wire. The kit
// owns the process, every handler and every rule that does not vary by vendor; a
// vendor supplies the ports below and nothing else.
//
// A port is named for what it achieves, never for the engine that achieves it,
// and no port signature carries an engine or vendor SDK type. This module's
// dependency list is the enforcement: the edge contract and the naming rules,
// nothing more.
package providerkit

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

// Provider is everything a vendor must supply. The accessors are the required
// port set: a vendor proves it is complete at compile time with
//
//	var _ providerkit.Provider = (*awsProvider)(nil)
//
// Optional behaviour is never a nil accessor — it is a method set found on a
// port by type assertion, so the kit can report precisely what is missing.
type Provider interface {
	// Vendor is the dispatch key the CLI already spells as a package name, and
	// the value the runtime half matches on when deciding which link types it
	// serves across the membrane.
	Vendor() Vendor

	// Accept decodes and validates the opaque options the project configured
	// this provider with. Returning a Refusal here is what the CLI renders as
	// "ocel.config.ts configures <package> with options it does not accept".
	Accept(ctx context.Context, options Options) error

	// Serves is the runtime half's promise, declared at deploy time: the link
	// types this vendor's membrane can answer for. The kit gates a manifest
	// against it and refuses a resource nothing will serve, instead of letting
	// the app find out at its first request.
	Serves() []LinkType

	Bootstrap() Bootstrapper
	Releases() Releaser
	Artifacts() ArtifactStore
	Records() RecordStore
	Values() ValueStore
	Credentials() Credentials
	Edges() EdgeRegistry
	DNS() DNSRegistry
}

// Optional method sets hang off Provider, never off a port.
//
// The reason is mechanical rather than aesthetic: a port can be wrapped. The
// moment a kit adapter composes one — resources.Releaser returning a fan-out over
// a vendor's per-primitive functions — an assertion against that port sees the
// wrapper and not the vendor, and a capability the vendor really has goes
// silently missing. Asserting against the root cannot be wrapped away.

// Warmer is optional: a vendor whose compute has a cold start may implement it,
// and the kit calls it after a release is promoted. A vendor that does not is not
// deficient, and conformance asserts nothing about it.
type Warmer interface {
	Warm(ctx context.Context, targets []string, report Reporter) error
}

// CodeEmbedder is optional: updating a deployed function's code in place, which
// is how a build's own artifacts reach a function that was provisioned before
// they existed. A vendor without in-place code update omits it, and the kit falls
// back to provisioning the artifact in.
type CodeEmbedder interface {
	EmbedCode(ctx context.Context, function string, artifact ArtifactRef, report Reporter) error
}

// Vendor names whose infrastructure a provider targets. It is vocabulary, not a
// switch: the kit prints it and matches the runtime half against it, and branches
// on nothing.
type Vendor string

// Options is the decoded form of the wire's opaque options struct. The kit hands
// the port the raw map and the vendor decodes it strictly; unknown keys are a
// refusal, not a warning.
type Options map[string]any

// Class is the dimension every account-scoped thing is partitioned by. The kit
// takes the edge contract's spelling rather than minting a second one.
type Class = edge.Class

const (
	ClassProduction = edge.ClassProduction
	ClassPreview    = edge.ClassPreview
)
