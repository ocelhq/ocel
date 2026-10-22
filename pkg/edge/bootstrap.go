package edge

import "strings"

type BootstrapOutput struct {
	Trust         TrustBoundary
	ReleasesStore *ReleasesStore
}

type Adoption struct {
	ReleasesStore bool
}

type BootstrapPart struct {
	Name    string
	Current bool
}

type PlanGroup struct {
	Kind    string
	Name    string
	Part    string
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
