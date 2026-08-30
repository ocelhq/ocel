package cloudfront

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const fakeStoreARN = "arn:aws:cloudfront::123456789012:key-value-store/routes"

func TestRoutesAreKeyedByTheHostnameLowercased(t *testing.T) {
	t.Parallel()

	w := newWorld()
	writer := routeWriter{clients: w.clients(), arn: fakeStoreARN}

	if err := writer.apply(context.Background(), map[string]route{
		"Shop.Example.com": {Origin: fakeEntryHost, Release: "d1.f1"},
	}, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, held := w.store.held(fakeStoreARN)["shop.example.com"]; !held {
		t.Fatalf("the store holds %v, want the hostname lowercased: the resolver looks a host up in lower case", slices.Sorted(keysOf(w.store.held(fakeStoreARN))))
	}

	if err := writer.apply(context.Background(), nil, []string{"SHOP.example.COM"}); err != nil {
		t.Fatalf("apply the delete: %v", err)
	}
	if held := w.store.held(fakeStoreARN); len(held) != 0 {
		t.Errorf("the store holds %v after the delete, want nothing: a route must not outlive the hostname however it was spelled", slices.Sorted(keysOf(held)))
	}
}

func TestAThrottledStoreIsWaitedOutRatherThanGivenUpOn(t *testing.T) {
	t.Parallel()

	t.Run("a throttled write is another attempt", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		bound(t, stack)
		staged(t, stack, fakeEntryURL, fakeAssetPrefix)
		w.store.throttles = 2

		if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardReporter()); err != nil {
			t.Fatalf("Promote: %v", err)
		}

		if published := routeOn(t, w, stack, boundHost); published.Origin != fakeEntryHost {
			t.Errorf("origin = %q, want the release to have landed once the throttling stopped", published.Origin)
		}
	})

	t.Run("a throttled version read is another attempt", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		w.store.describeErr = throttlingError()
		writer := routeWriter{
			clients: w.clients(),
			arn:     fakeStoreARN,
			wait:    func(context.Context, time.Duration) error { return nil },
		}

		err := writer.apply(context.Background(), map[string]route{boundHost: {Origin: fakeEntryHost}}, nil)

		if err == nil {
			t.Fatal("apply error = nil, want the refusal a store that never answers gives")
		}
		if got := w.store.count("kvs.DescribeKeyValueStore"); got != routeAttempts {
			t.Errorf("DescribeKeyValueStore calls = %d, want %d: a throttle is one attempt, not the end of them", got, routeAttempts)
		}
		if !strings.Contains(err.Error(), "promote again") {
			t.Errorf("err = %q, want it to say what to do next", err)
		}
	})
}
