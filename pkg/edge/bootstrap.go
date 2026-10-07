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

type BootstrapPart struct {
	Name    string
	Current bool
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

const OriginGroupName = "origin"

func EdgeGroupName(kind Kind) string {
	if kind == None {
		return OriginGroupName
	}
	return string(kind) + "/edge"
}

func EdgeGroupKindOf(name string) (Kind, bool) {
	kind, ok := strings.CutSuffix(name, "/edge")
	if !ok || kind == "" {
		return "", false
	}
	return Kind(kind), true
}

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

const OfferReleasesStore OfferKind = "releases-store"

const OfferISRWriter OfferKind = "isr-writer"

const OfferWorkerClientCertificate OfferKind = "worker-client-certificate"

const (
	OfferKeyClientCertificateID          = "certificateId"
	OfferKeyClientCertificateAuthorities = "certificateAuthorities"
)

const (
	OfferKeyISRWriterEndpoint            = "endpoint"
	OfferKeyISRWriterScriptName          = "scriptName"
	OfferKeyISRWriterBootstrapCredential = "bootstrapCred"
)

const (
	OfferKeyStoreEndpoint            = "endpoint"
	OfferKeyStoreScriptName          = "scriptName"
	OfferKeyStoreBootstrapCredential = "bootstrapCred"
)

const (
	OfferKeyBucket          = "bucket"
	OfferKeyEndpoint        = "endpoint"
	OfferKeyRegion          = "region"
	OfferKeyAccessKeyID     = "accessKeyId"
	OfferKeySecretAccessKey = "secretAccessKey"
)
