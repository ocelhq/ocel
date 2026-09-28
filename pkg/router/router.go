package router

import "time"

const DefaultPointer = "@production"

type FlipBound struct {
	Typical   time.Duration `json:"typical"`
	Published bool          `json:"published"`
}
