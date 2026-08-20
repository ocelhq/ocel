package deploy

type HistoryEntry struct {
	Promotion
	Active bool
}

func Retention(history []HistoryEntry, keepN int) (keep, collect []string) {
	for i, h := range history {
		if i < keepN || h.Active {
			keep = append(keep, h.PromotionID)
		} else {
			collect = append(collect, h.PromotionID)
		}
	}
	return keep, collect
}
