package worker

import (
	"net/http"

	"ocel.dev/internal/delivery"
)

// Handler serves the deliveries Ocel makes to the worker named name: the
// tasks and consumers placed on it. Ocel's generated worker entry mounts it,
// so an app does not.
//
// It answers 200 with the run's output as JSON when the run succeeds, 422
// when it is aborted, and 500 when it is to be retried. A cancelled request
// cancels the context the run is handed and calls a task's OnCancel hook in
// place of the other lifecycle hooks; the answer is still what the run did.
func Handler(name string) http.Handler {
	return delivery.Handler(name)
}
