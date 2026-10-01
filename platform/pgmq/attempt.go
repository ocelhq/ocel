package pgmq

import (
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
)

type Attempt struct {
	Topic       string
	Consumer    string
	IsTask      bool
	Execution   string
	Number      int
	MaxAttempts int
	Status      provider.RunStatus
	Took        time.Duration
	Reason      string
}
