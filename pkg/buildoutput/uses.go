package buildoutput

type Uses struct {
	ISR               bool
	ImageOptimization bool
}

func (u Uses) HasAnyOf(other Uses) bool {
	return u.ISR && other.ISR || u.ImageOptimization && other.ImageOptimization
}

func PresumeUses(framework string) Uses {
	if framework == FrameworkNext {
		return Uses{ISR: true, ImageOptimization: true}
	}
	return Uses{}
}
