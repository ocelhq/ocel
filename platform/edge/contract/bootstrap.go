package edge

import "context"

type BootstrapOutput struct {
	Trust  TrustBoundary
	Values map[string]string
	Offers []Offer
}

type BootstrapPlanner interface {
	PlanBootstrap(ctx context.Context, class Class) ([]PlanChange, error)
}

type BootstrapAdopter interface {
	Adoption(ctx context.Context, class Class) (Adoption, error)
}

type Adoption struct {
	Values map[string]string
	Offers []OfferKind
}

type PlanChange struct {
	Kind   string
	Name   string
	Action PlanAction
	Reason string
}

type PlanAction string

const (
	PlanCreate PlanAction = "create"
	PlanUpdate PlanAction = "update"
	PlanKeep   PlanAction = "keep"
)

func ValidPlanAction(action PlanAction) bool {
	switch action {
	case PlanCreate, PlanUpdate, PlanKeep:
		return true
	}
	return false
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
