package topics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/pkg/runs"
)

const (
	pushPrefix    = "/topics/"
	consumerInfix = "/consumers/"
)

type Deliveries struct {
	Engine *runs.Engine
}

type pushRequest struct {
	Message pubsub.PubsubMessage `json:"message"`
}

func (d Deliveries) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	topic, consumer, routed := pushedTo(r)
	if !routed {
		http.Error(w, fmt.Sprintf("%s %s is not a push to a consumer", r.Method, r.URL.Path), http.StatusNotFound)
		return
	}
	var pushed pushRequest
	if err := json.NewDecoder(r.Body).Decode(&pushed); err != nil {
		http.Error(w, fmt.Sprintf("the push is not a Pub/Sub message: %v", err), http.StatusBadRequest)
		return
	}
	msg, err := messageOf(&pushed.Message, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dispositions := d.Engine.Deliver(r.Context(), topic, consumer, []runs.Received{{Message: msg}})
	if !dispositions[0].Done {
		http.Error(w, fmt.Sprintf("message %s is redelivered: its run is not settled", msg.ID), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pushedTo(r *http.Request) (string, string, bool) {
	if r.Method != http.MethodPost {
		return "", "", false
	}
	rest, found := strings.CutPrefix(r.URL.Path, pushPrefix)
	if !found {
		return "", "", false
	}
	topic, consumer, found := strings.Cut(rest, consumerInfix)
	return topic, consumer, found && topic != "" && consumer != "" && !strings.Contains(consumer, "/")
}
