package edge

// BootstrapOutput is what an edge reports after standing itself up. The
// provider persists Values and hands them back at deploy time, so the edge can
// read back what it provisioned without re-querying its own API.
type BootstrapOutput struct {
	// Trust is where the edge runs relative to the provider's trust boundary. It
	// decides whether the provider mints static credentials for it at all.
	Trust TrustBoundary
	// Values are the edge's own bootstrap outputs, opaque to the provider.
	Values map[string]string
	// Offers are resources the edge provisioned that the provider may adopt in
	// place of its own.
	Offers []Offer
}

// TrustBoundary is where an edge runs relative to the provider's trust
// boundary. An edge outside it can only be given static credentials; an edge
// inside it uses the provider's native identity, so no long-lived credential is
// created.
type TrustBoundary string

const (
	// TrustExternal is an edge running in a third party's account.
	TrustExternal TrustBoundary = "external"
	// TrustInternal is an edge running inside the provider's own account.
	TrustInternal TrustBoundary = "internal"
)

// Offer is a resource the edge provisioned that the provider may adopt instead
// of provisioning its own equivalent. A provider ignores any Kind it does not
// recognise rather than failing, so a newer edge paired with an older provider
// degrades instead of breaking.
type Offer struct {
	Kind   OfferKind
	Values map[string]string
}

// OfferKind names what an Offer is, so a provider can decide whether it
// understands the offer before reading its Values.
type OfferKind string

// OfferCacheStore offers an object store and tag clock the provider may back a
// framework's incremental cache with.
const OfferCacheStore OfferKind = "cache-store"
