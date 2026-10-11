package alb

type StaticRoutes struct {
	Backend    string   `json:"backend"`
	Objects    string   `json:"objects"`
	Immutable  []string `json:"immutable"`
	Revalidate []string `json:"revalidate,omitempty"`
}
