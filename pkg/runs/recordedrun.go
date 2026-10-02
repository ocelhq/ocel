package runs

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
)

type recordedRun struct {
	Run string `json:"run"`
}

func NewRecordNamingRun(purpose provider.RecordPurpose, task, key, execution string, expires time.Time) provider.ExpiringRecord {
	value, _ := json.Marshal(recordedRun{Run: execution})
	return provider.ExpiringRecord{Purpose: purpose, Topic: task, Key: key, Value: value, ExpiresAt: expires}
}

func ReadRecordedRun(record provider.ExpiringRecord) (string, error) {
	var recorded recordedRun
	if err := json.Unmarshal(record.Value, &recorded); err != nil {
		return "", fmt.Errorf("read the run the %s record %q of %s names: %w", record.Purpose, record.Key, record.Topic, err)
	}
	return recorded.Run, nil
}
