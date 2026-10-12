package buildoutput

type Uses struct {
	Revalidation      bool
	ImageOptimization bool
}

func (u Uses) HasAnyOf(other Uses) bool {
	return u.Revalidation && other.Revalidation || u.ImageOptimization && other.ImageOptimization
}

func PresumeUses(framework string) Uses {
	if framework == FrameworkNext {
		return Uses{Revalidation: true, ImageOptimization: true}
	}
	return Uses{}
}
