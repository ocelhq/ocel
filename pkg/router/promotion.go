package router

import "github.com/ocelhq/ocel/pkg/edge"

type Promotion struct {
	PromotionID string             `json:"promotionId"`
	Ts          int64              `json:"ts"`
	Builds      map[string]string  `json:"builds"`
	Tag         string             `json:"tag,omitempty"`
	Propagation *Propagation       `json:"propagation,omitempty"`
	Hosts       []edge.PreviewHost `json:"hosts,omitempty"`
}

type HistoryEntry struct {
	Promotion
	Active     bool `json:"active"`
	Unpromoted bool `json:"unpromoted"`
}

type PruneResult struct {
	KeptPromotionIDs           []string         `json:"keptPromotionIds"`
	RemovedPromotionIDs        []string         `json:"removedPromotionIds"`
	UnnamedRecordKeys          []string         `json:"unnamedRecordKeys"`
	SurvivingRecordKeys        []string         `json:"survivingRecordKeys"`
	SurvivingPointerRecordKeys []string         `json:"survivingPointerRecordKeys"`
	DeploymentRemovals         []PointerRemoval `json:"deploymentRemovals,omitempty"`
}
