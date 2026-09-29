package switchboard

const PlaceEnv = "OCEL_SWITCHBOARD_PLACE"

type SiblingFile struct {
	Name    string `json:"name"`
	Content []byte `json:"content"`
}
