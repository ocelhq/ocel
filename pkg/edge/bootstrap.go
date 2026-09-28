package edge

import "strings"

type BootstrapOutput struct {
	Trust  TrustBoundary
	Values map[string]string
	Offers []Offer
}

type Adoption struct {
	Values map[string]string
	Offers []OfferKind
}

type PlanGroup struct {
	Kind    string
	Name    string
	Feature string
	Action  PlanAction
	Reason  string
	Slow    bool
	Changes []PlanChange
}

type PlanChange struct {
	Kind   string
	Name   string
	Action PlanAction
	Reason string
	Slow   bool
}

type PlanAction string

const (
	PlanCreate            PlanAction = "create"
	PlanUpdate            PlanAction = "update"
	PlanDelete            PlanAction = "delete"
	PlanDisableThenDelete PlanAction = "disable-then-delete"
	PlanKeep              PlanAction = "keep"
)

func ValidPlanAction(action PlanAction) bool {
	switch action {
	case PlanCreate, PlanUpdate, PlanDelete, PlanDisableThenDelete, PlanKeep:
		return true
	}
	return false
}

const EdgeGroupKind = "edge"

func EdgeGroupName(kind Kind) string { return string(kind) + "/edge" }

func EdgeGroupKindOf(name string) (Kind, bool) {
	kind, ok := strings.CutSuffix(name, "/edge")
	if !ok || kind == "" {
		return "", false
	}
	return Kind(kind), true
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
