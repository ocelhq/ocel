package router

type Promotion struct {
	PromotionID string            `json:"promotionId"`
	Ts          int64             `json:"ts"`
	Builds      map[string]string `json:"builds"`
	Tag         string            `json:"tag,omitempty"`
	Flip        *FlipBound        `json:"flip,omitempty"`
}

type HistoryEntry struct {
	Promotion
	Active bool `json:"active"`
}

type PruneResult struct {
	KeptPromotionIDs           []string `json:"keptPromotionIds"`
	RemovedPromotionIDs        []string `json:"removedPromotionIds"`
	RemovedRecordKeys          []string `json:"removedRecordKeys"`
	SurvivingRecordKeys        []string `json:"survivingRecordKeys"`
	SurvivingPointerRecordKeys []string `json:"survivingPointerRecordKeys"`
}
