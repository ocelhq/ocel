package runs

import "testing"

func TestAReservationHoldsOnlyWhatBothItsConsumerAndItsWorkerHaveFreeAndGivesBackWhatGoesUnused(t *testing.T) {
	t.Parallel()
	consumer, worker := newLocalSlots(5), newLocalSlots(2)

	held := reserveSlots(4, consumer, worker)
	if held.n != 2 || consumer.used != 2 || worker.used != 2 {
		t.Fatalf("reserved %d with the consumer using %d and the worker %d, want 2 of each: the worker has room for 2", held.n, consumer.used, worker.used)
	}
	held.keep(1)
	if consumer.used != 1 || worker.used != 1 {
		t.Errorf("after keeping one, the consumer uses %d and the worker %d, want 1 each", consumer.used, worker.used)
	}
	held.release(1)
	if consumer.used != 0 || worker.used != 0 {
		t.Errorf("after the delivery ended, the consumer uses %d and the worker %d, want none", consumer.used, worker.used)
	}
	for i, changed := range held.changed {
		select {
		case <-changed:
		default:
			t.Errorf("slots %d never said a slot came free", i)
		}
	}
}
