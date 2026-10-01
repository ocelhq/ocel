package pgmq

import (
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
)

type Attempt struct {
	Topic     string
	Consumer  string
	Task      bool
	Execution string
	Number    int
	Of        int
	Status    provider.RunStatus
	Took      time.Duration
	Reason    string
}
