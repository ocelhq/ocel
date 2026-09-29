package switchboard

const (
	PlaceEnv     = "OCEL_SWITCHBOARD_PLACE"
	OriginPrefix = "ocel-origin-"
)

type SiblingFile struct {
	Name    string `json:"name"`
	Content []byte `json:"content"`
}
