package edge

type BootstrapOutput struct {
	Trust  TrustBoundary
	Values map[string]string
	Offers []Offer
}

type Class string

const (
	ClassProduction Class = "production"
	ClassPreview    Class = "preview"
)

type TrustBoundary string

const (
	TrustExternal TrustBoundary = "external"
	TrustInternal TrustBoundary = "internal"
)

type Offer struct {
	Kind   OfferKind
	Values map[string]string
}

type OfferKind string

const OfferCacheStore OfferKind = "cache-store"

const OfferDeploymentsStore OfferKind = "deployments-store"

const OfferISRWriter OfferKind = "isr-writer"

const (
	OfferKeyISRWriterEndpoint      = "endpoint"
	OfferKeyISRWriterScriptName    = "scriptName"
	OfferKeyISRWriterBootstrapCred = "bootstrapCred"
)

const (
	OfferKeyStoreEndpoint      = "endpoint"
	OfferKeyStoreScriptName    = "scriptName"
	OfferKeyStoreBootstrapCred = "bootstrapCred"
)

const (
	OfferKeyBucket          = "bucket"
	OfferKeyEndpoint        = "endpoint"
	OfferKeyRegion          = "region"
	OfferKeyAccessKeyID     = "accessKeyId"
	OfferKeySecretAccessKey = "secretAccessKey"
)
