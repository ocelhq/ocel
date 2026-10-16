package buildoutput

type Uses struct {
	Revalidation      bool
	ImageOptimization bool
}

func PresumeUses(framework string) Uses {
	if framework == FrameworkNext {
		return Uses{Revalidation: true, ImageOptimization: true}
	}
	return Uses{}
}
