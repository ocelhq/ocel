package googletest

import (
	"encoding/json"
	"net/http"
)

func ReadBody[T any](w http.ResponseWriter, r *http.Request) *T {
	var body T
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	return &body
}

func WriteBody(w http.ResponseWriter, body any) {
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(err)
	}
}
