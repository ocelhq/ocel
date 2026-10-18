package edge

type PurgeBudget struct {
	TagsPerPurge int
}

func (b PurgeBudget) IsZero() bool {
	return b == PurgeBudget{}
}
