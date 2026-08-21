package providerkit

import (
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

// EdgeRegistry is which edges this vendor's apps can be fronted with. It is a
// registry and nothing else: the kit forwards promotions, rollbacks, preview
// wildcards and hostname binding to the edge contract itself, which #390 owns and
// this kit composes as it stands.
type EdgeRegistry interface {
	Supported() []edge.Kind

	Default() edge.Kind

	// Open builds the edge. An unknown kind is a Refusal with CodeInvalid, listing
	// what Supported returned.
	Open(kind edge.Kind) (edge.Edge, error)
}

// LinkType is one kind of resource an app can link, as the runtime half spells
// it. A provider declaring a type here is promising its membrane can serve it.
type LinkType string
