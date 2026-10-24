//go:build integration

package pgmq

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/runs"
)

func TestWaitingMessagesAreDeliveredFromEachLaneByWeightWithoutStarvingLow(t *testing.T) {
	worker := newWorker(t, func(*topicv1.Envelope) reply { return reply{status: http.StatusOK, hold: 10 * time.Millisecond} })
	engine := applied(t, map[string]*contractv1.ManifestTopic{
		"orders": aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "worker", Concurrency: 1}),
	}, map[string]Worker{"worker": {URL: worker.server.URL}})
	for _, lane := range []topicv1.Lane{topicv1.Lane_LANE_LOW, topicv1.Lane_LANE_DEFAULT, topicv1.Lane_LANE_HIGH} {
		for n := range 10 {
			send(t, engine, "orders", fmt.Sprintf(`{"lane":%q,"n":%d}`, runs.LaneOf(lane), n), &topicv1.SendRequest{Lane: lane})
		}
	}
	startDispatch(t, engine)

	got := awaitDelivered(t, worker, 10)
	counts := map[string]int{}
	for _, d := range got[:10] {
		lane, _ := fieldOf(d.envelope.GetPayload(), "lane").(string)
		counts[lane]++
	}
	if counts["high"] != 6 || counts["default"] != 3 || counts["low"] != 1 {
		t.Errorf("the first 10 deliveries by lane = %v, want 6 high, 3 default, 1 low", counts)
	}
}

func TestALaneReadFindsItsLanesMessagesThroughAnIndexRatherThanEveryMessageInTheQueue(t *testing.T) {
	ctx := context.Background()
	engine := applied(t, map[string]*contractv1.ManifestTopic{"orders": aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "mail"})}, nil)
	var table string
	if err := engine.pool.QueryRow(ctx, "SELECT pgmq.format_table_name($1, 'q')", queueName("orders", "email")).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.pool.Exec(ctx, `INSERT INTO pgmq.`+table+` (vt, message)
		SELECT clock_timestamp(), jsonb_build_object('execution', i::text, 'lane', CASE WHEN i % 100 = 0 THEN 'high' ELSE 'default' END)
		FROM generate_series(1, 20000) i`); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.pool.Exec(ctx, "ANALYZE pgmq."+table); err != nil {
		t.Fatal(err)
	}

	rows, err := engine.pool.Query(ctx, `EXPLAIN SELECT msg_id FROM pgmq.`+table+`
		WHERE vt <= clock_timestamp() AND CASE WHEN '{"lane":"high"}' != '{}'::jsonb THEN (message @> '{"lane":"high"}')::integer ELSE 1 END = 1
		ORDER BY msg_id ASC LIMIT 10 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan, "\n"); !strings.Contains(got, table+"_high_idx") {
		t.Errorf("pgmq.read of the high lane plans\n%s\nwant it to scan the queue's high-lane index", got)
	}
}
