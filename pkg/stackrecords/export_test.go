package stackrecords

import "time"

func init() {
	partPoll = time.Millisecond
	partLease = 50 * time.Millisecond
}
