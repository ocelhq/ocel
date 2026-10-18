package edge

type CacheTagLimits struct {
	TagBytes    int
	Tags        int
	HeaderBytes int
}

func (l CacheTagLimits) IsZero() bool {
	return l == CacheTagLimits{}
}
