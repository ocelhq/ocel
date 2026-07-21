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

// Class is the substrate an edge is bootstrapping alongside. Production and
// preview are separate substrates that must never share state, so anything an
// edge provisions is provisioned once per class.
type Class string

const (
	ClassProduction Class = "production"
	ClassPreview    Class = "preview"
)

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

// OfferDeploymentsStore offers the shared deployments-store worker the edge
// provisioned once at bootstrap: the address, credential and script name every
// project's root stack needs to seed and reach its own instance.
const OfferDeploymentsStore OfferKind = "deployments-store"

// Keys of an OfferDeploymentsStore's Values.
const (
	// OfferKeyStoreEndpoint is the shared store worker's HTTP endpoint.
	OfferKeyStoreEndpoint = "endpoint"
	// OfferKeyStoreScriptName is the shared store worker's script name, which a
	// project's generic worker service-binds to.
	OfferKeyStoreScriptName = "scriptName"
	// OfferKeyStoreBootstrapCred is the account-level bootstrap credential that
	// authorizes seeding/rotating a project's instance (/<slug>/initialize).
	OfferKeyStoreBootstrapCred = "bootstrapCred"
)

// Keys of an OfferCacheStore's Values. They describe the store in
// S3-compatible terms — endpoint, region, bucket, static keys — so a provider
// adopts it with the object-store client it already has rather than learning
// one edge's API.
const (
	OfferKeyBucket      = "bucket"
	OfferKeyEndpoint    = "endpoint"
	OfferKeyRegion      = "region"
	OfferKeyAccessKeyID = "accessKeyId"
	// OfferKeySecretAccessKey is present only when the edge minted the key on
	// this run. An edge whose API cannot read a credential back reoffers the
	// store without it, so the provider keeps the secret it already holds and can
	// tell a reused credential from an unstored one by comparing access key ids.
	OfferKeySecretAccessKey = "secretAccessKey"
)
