package devserver

import "testing"

func TestEnvFanout(t *testing.T) {
	t.Parallel()

	t.Run("a subscriber that has not read yet receives the newest env pushed", func(t *testing.T) {
		t.Parallel()

		fanout := newEnvFanout()
		ch := fanout.subscribe()
		fanout.push(map[string]string{"KEY": "first"})
		fanout.push(map[string]string{"KEY": "second"})

		if got := (<-ch)["KEY"]; got != "second" {
			t.Fatalf("subscriber received KEY=%q, want the newest push %q", got, "second")
		}
	})
}
