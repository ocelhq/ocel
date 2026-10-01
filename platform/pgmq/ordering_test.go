package pgmq

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type keyedLog struct {
	mu        sync.Mutex
	inFlight  map[string]int
	overlap   []string
	order     map[string][]int
	parallel  bool
	running   int
	failFirst map[string]int
}

func (l *keyedLog) respond(envelope *topicv1.Envelope) reply {
	key, _ := fieldOf(envelope.GetPayload(), "key").(string)
	number, _ := fieldOf(envelope.GetPayload(), "n").(float64)
	n := int(number)
	l.mu.Lock()
	l.inFlight[key]++
	l.running++
	if l.inFlight[key] > 1 {
		l.overlap = append(l.overlap, key)
	}
	if l.running > 1 {
		l.parallel = true
	}
	fail := n == 0 && l.failFirst[key] > 0
	if fail {
		l.failFirst[key]--
	} else {
		l.order[key] = append(l.order[key], n)
	}
	l.mu.Unlock()

	time.Sleep(80 * time.Millisecond)

	l.mu.Lock()
	l.inFlight[key]--
	l.running--
	l.mu.Unlock()
	if fail {
		return reply{status: http.StatusInternalServerError, body: "not yet"}
	}
	return reply{status: http.StatusOK}
}

func TestAnOrderedTaskRunsOneRunPerKeyAtATimeInTriggerOrderWhileKeysRunInParallel(t *testing.T) {
	log := &keyedLog{inFlight: map[string]int{}, order: map[string][]int{}, failFirst: map[string]int{"a": 2}}
	engine, _ := aServedTask(t, log.respond, retrying(5, 100*time.Millisecond, 100*time.Millisecond), func(topic *contractv1.ManifestTopic) {
		topic.Ordered = true
	})

	var ids []string
	for n := range 4 {
		for _, key := range []string{"a", "b"} {
			ids = append(ids, trigger(t, engine, "resize", fmt.Sprintf(`{"key":%q,"n":%d}`, key, n), &taskv1.TriggerOptions{Key: key}))
		}
	}
	for _, id := range ids {
		awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)
	}

	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.overlap) > 0 {
		t.Errorf("keys %v had two runs in flight at once", log.overlap)
	}
	for _, key := range []string{"a", "b"} {
		if got := fmt.Sprint(log.order[key]); got != "[0 1 2 3]" {
			t.Errorf("key %s completed in order %s, want [0 1 2 3]: a failed run holds its key until it succeeds", key, got)
		}
	}
	if !log.parallel {
		t.Error("no two keys ever ran at once")
	}
}

func TestAnOrderedTaskRunsTriggersWithoutAKeyInParallel(t *testing.T) {
	log := &keyedLog{inFlight: map[string]int{}, order: map[string][]int{}, failFirst: map[string]int{}}
	engine, _ := aServedTask(t, log.respond, func(topic *contractv1.ManifestTopic) { topic.Ordered = true })

	var ids []string
	for n := range 4 {
		ids = append(ids, trigger(t, engine, "resize", fmt.Sprintf(`{"key":"k%d","n":%d}`, n, n), nil))
	}
	for _, id := range ids {
		awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)
	}
	if !log.parallel {
		t.Error("runs without a key waited on one another, as if they shared one")
	}
}
