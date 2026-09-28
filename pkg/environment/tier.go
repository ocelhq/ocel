package environment

type Tier string

const (
	TierProduction Tier = "production"
	TierPreview    Tier = "preview"
)

func (t Tier) Sibling() Tier {
	switch t {
	case TierProduction:
		return TierPreview
	case TierPreview:
		return TierProduction
	default:
		return ""
	}
}
