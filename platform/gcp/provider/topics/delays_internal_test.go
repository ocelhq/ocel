package topics

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/envelope"
)

const delayQueue = "projects/acme/locations/us-central1/queues/ocel-preview-delays"

var taskID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,500}$`)

func delayTaskIDs(t *testing.T, count int) []string {
	t.Helper()
	deployment := Deployment{Delays: Delays{Queue: delayQueue}}
	published := time.Now().UTC()
	ids := make([]string, 0, count)
	for i := range count {
		at := published.Add(time.Duration(i) * time.Millisecond)
		name := deployment.delayTaskOf(publication{messageID: envelope.NewMessageID(at), publishedAt: at, dueAt: at.Add(time.Hour)})
		id, found := strings.CutPrefix(name, delayQueue+"/tasks/")
		if !found || !taskID.MatchString(id) {
			t.Fatalf("the delay task is named %q, want a Cloud Tasks task ID on queue %s", name, delayQueue)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestTheDelayTasksOfMessagesPublishedInOrderAreNotNamedInThatOrder(t *testing.T) {
	t.Parallel()
	ids := delayTaskIDs(t, 32)

	if slices.IsSorted(ids) {
		t.Errorf("the delay tasks of 32 messages are named in their publish order %v, want names spread by a hash", ids)
	}
	if prefix := ids[0][:4]; !slices.ContainsFunc(ids, func(id string) bool { return !strings.HasPrefix(id, prefix) }) {
		t.Errorf("every delay task name opens with %q, want names spread by a hash", prefix)
	}
}
