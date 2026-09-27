package switchboard

const PlaceEnv = "OCEL_SWITCHBOARD_PLACE"

type Neighbour struct {
	Name    string `json:"name"`
	Content []byte `json:"content"`
}
