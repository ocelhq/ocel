package topic

import "testing"

func TestATopicRecordTheBoxKeptCompactIsTheConfigABuildSpacedOut(t *testing.T) {
	kept := []byte(`{"schema":"orders","ordered":true,"ttl":"86400s"}`)
	for _, wanted := range [][]byte{
		[]byte(`{"schema":"orders","ordered":true,"ttl":"86400s"}`),
		[]byte(`{"schema": "orders", "ordered": true, "ttl": "86400s"}`),
	} {
		if !sameRecord(kept, wanted) {
			t.Errorf("sameRecord(%s, %s) = false, want the record the box compacted to match the config it was written from", kept, wanted)
		}
	}
	if sameRecord(kept, []byte(`{"schema":"orders","ordered":false,"ttl":"86400s"}`)) {
		t.Error("sameRecord() of a config whose ordering changed = true, want the change planned")
	}
}
