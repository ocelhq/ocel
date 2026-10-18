package edge

type PurgeBudget struct {
	PurgesPerMinute int
	TagsPerPurge    int
}

func (b PurgeBudget) IsZero() bool {
	return b == PurgeBudget{}
}
