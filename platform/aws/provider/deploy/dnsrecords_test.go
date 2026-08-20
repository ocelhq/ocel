package deploy

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type recordingDNS struct {
	written []edge.Record
	deleted []edge.Record
}

func (r *recordingDNS) EnsureRecords(_ context.Context, records []edge.Record, _ func(string)) ([]edge.Record, error) {
	r.written = append(r.written, records...)
	return records, nil
}

func (r *recordingDNS) DeleteRecords(_ context.Context, records []edge.Record) error {
	r.deleted = append(r.deleted, records...)
	return nil
}

type recordingWaiter struct {
	awaited []edge.Record
}

func (r *recordingWaiter) Await(_ context.Context, records []edge.Record, _ func(string)) error {
	r.awaited = append(r.awaited, records...)
	return nil
}

func TestSettleStackRecords(t *testing.T) {
	t.Parallel()

	specs := []edge.StackSpec{{Domains: []string{"shop.app.com"}}, {Domains: nil}}
	wildcard := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}

	t.Run("a writer writes every declared host and records it on the stack", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{kind: cloudflare.Kind}, DNS: writer}

		state, err := settleStackRecords(t.Context(), cfg, specs, edge.StackState{Slug: "shop"}, func(string) {})
		if err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(writer.written) != 1 || writer.written[0] != wildcard {
			t.Errorf("written = %v, want %v", writer.written, wildcard)
		}
		if got := state.Records; len(got) != 1 || got[0] != wildcard {
			t.Errorf("recorded records = %v, want %v", got, wildcard)
		}
	})

	t.Run("an edge with a front per host writes each host its own record", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{kind: apigateway.Kind}, DNS: writer}
		var state edge.StackState
		state.PublishFront("shop.app.com", "d-shop.execute-api.eu-west-1.amazonaws.com")
		state.PublishFront("www.app.com", "d-www.execute-api.eu-west-1.amazonaws.com")
		state.Bind("shop.app.com")
		state.Bind("www.app.com")

		if _, err := settleStackRecords(t.Context(), cfg, []edge.StackSpec{{Domains: []string{"shop.app.com", "www.app.com"}}}, state, func(string) {}); err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		want := []edge.Record{
			{Name: "shop.app.com", Type: edge.RecordTypeCNAME, Value: "d-shop.execute-api.eu-west-1.amazonaws.com"},
			{Name: "www.app.com", Type: edge.RecordTypeCNAME, Value: "d-www.execute-api.eu-west-1.amazonaws.com"},
		}
		if !slices.Equal(writer.written, want) {
			t.Errorf("written = %v, want %v", writer.written, want)
		}
	})

	t.Run("a bound host the edge published no front for is refused by name", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Edge: &recordingEdge{kind: apigateway.Kind}, DNS: &recordingDNS{}}

		var bound edge.StackState
		bound.Bind("shop.app.com")

		_, err := settleStackRecords(t.Context(), cfg, specs, bound, func(string) {})
		if err == nil {
			t.Fatal("settleStackRecords err = nil, want a refusal: nothing to point the host at")
		}
		if !strings.Contains(err.Error(), "shop.app.com") {
			t.Errorf("err = %v, want it to name the host with no front", err)
		}
	})

	t.Run("a declared host no edge has bound yet is left for the command that binds it", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{kind: apigateway.Kind}, DNS: writer}

		var said []string
		state, err := settleStackRecords(t.Context(), cfg, specs, edge.StackState{}, func(m string) { said = append(said, m) })
		if err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(writer.written) != 0 {
			t.Errorf("written = %v, want none: nothing serves the host yet", writer.written)
		}
		if len(said) != 1 || !strings.Contains(said[0], "shop.app.com") || !strings.Contains(said[0], "ocel domain add") {
			t.Errorf("said = %v, want the pending host and the command that binds it named", said)
		}
		if state.Records != nil {
			t.Errorf("state = %+v, want no records recorded", state)
		}
	})

	t.Run("a preview wildcard no command can bind is refused with the remedy that serves it", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Edge: &recordingEdge{kind: apigateway.Kind}, DNS: &recordingDNS{}}

		_, err := settleStackRecords(t.Context(), cfg, []edge.StackSpec{{Domains: []string{"*.preview.app.com"}}}, edge.StackState{}, func(string) {
			t.Error("said something about a wildcard the deploy refuses")
		})
		if err == nil {
			t.Fatal("settleStackRecords err = nil, want a refusal: no command binds a wildcard here")
		}
		if !strings.Contains(err.Error(), "*.preview.app.com") || !strings.Contains(err.Error(), "ocel domain use") {
			t.Errorf("err = %v, want the wildcard and the command that serves previews named", err)
		}
	})

	t.Run("without a writer the record is waited on and nothing is recorded", func(t *testing.T) {
		t.Parallel()

		waiter := &recordingWaiter{}
		cfg := Config{Edge: &recordingEdge{kind: cloudflare.Kind}, DNSAwait: waiter}

		state, err := settleStackRecords(t.Context(), cfg, specs, edge.StackState{}, func(string) {})
		if err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(waiter.awaited) != 1 || waiter.awaited[0] != wildcard {
			t.Errorf("awaited = %v, want %v", waiter.awaited, wildcard)
		}
		if state.Records != nil {
			t.Errorf("state = %+v, want no records recorded: ocel wrote none", state)
		}
	})

	t.Run("a stack serving no hostname of its own writes nothing", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{kind: cloudflare.Kind}, DNS: writer}

		if _, err := settleStackRecords(t.Context(), cfg, []edge.StackSpec{{}}, edge.StackState{}, func(string) {}); err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(writer.written) != 0 {
			t.Errorf("written = %v, want none", writer.written)
		}
	})

	t.Run("a non-cloudflare edge with no front to name is refused", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Edge: &recordingEdge{kind: cloudfront.Kind}, DNS: &recordingDNS{}}

		var bound edge.StackState
		bound.Bind("shop.app.com")

		if _, err := settleStackRecords(t.Context(), cfg, specs, bound, func(string) {}); err == nil {
			t.Fatal("settleStackRecords err = nil, want a refusal: nothing to point the hostname at")
		}
	})

	t.Run("a non-cloudflare edge points a bound hostname at the front its stack published", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{kind: cloudfront.Kind}, DNS: writer}
		state := edge.StackState{Front: "d111111abcdef8.cloudfront.net"}
		state.Bind("shop.app.com")

		if _, err := settleStackRecords(t.Context(), cfg, specs, state, func(string) {}); err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		want := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeCNAME, Value: "d111111abcdef8.cloudfront.net"}
		if len(writer.written) != 1 || writer.written[0] != want {
			t.Errorf("written = %v, want %v", writer.written, want)
		}
	})

	t.Run("a declared host the shared front cannot serve yet is left unpointed", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{kind: cloudfront.Kind}, DNS: writer}
		state := edge.StackState{Front: "d111111abcdef8.cloudfront.net"}

		var said []string
		settled, err := settleStackRecords(t.Context(), cfg, specs, state, func(m string) { said = append(said, m) })
		if err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(writer.written) != 0 {
			t.Errorf("written = %v, want none: the distribution answers for no such hostname yet", writer.written)
		}
		if len(said) != 1 || !strings.Contains(said[0], "shop.app.com") || !strings.Contains(said[0], "ocel domain add") {
			t.Errorf("said = %v, want the pending host and the command that binds it named", said)
		}
		if settled.Records != nil {
			t.Errorf("state = %+v, want no records recorded", settled)
		}
	})

	t.Run("cloudflare points a host no command has bound, because the record is what binds it", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{kind: cloudflare.Kind}, DNS: writer}

		if _, err := settleStackRecords(t.Context(), cfg, specs, edge.StackState{}, func(m string) {
			t.Errorf("said %q about a host cloudflare serves from the record alone", m)
		}); err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(writer.written) != 1 || writer.written[0] != wildcard {
			t.Errorf("written = %v, want %v", writer.written, wildcard)
		}
	})

	t.Run("a hostname dropped from the deploy has its record deleted", func(t *testing.T) {
		t.Parallel()

		gone := edge.Record{Name: "old.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
		var prior edge.StackState
		prior.RecordWrites([]edge.Record{wildcard, gone})
		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{kind: cloudflare.Kind}, DNS: writer, StackState: prior}

		state, err := settleStackRecords(t.Context(), cfg, specs, prior, func(string) {})
		if err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(writer.deleted) != 1 || writer.deleted[0] != gone {
			t.Errorf("deleted = %v, want %v", writer.deleted, gone)
		}
		if kept := state.Records; len(kept) != 1 || kept[0] != wildcard {
			t.Errorf("recorded records = %v, want only %v", kept, wildcard)
		}
	})

	t.Run("a stack serving nothing any more gives up every record it wrote", func(t *testing.T) {
		t.Parallel()

		var prior edge.StackState
		prior.RecordWrites([]edge.Record{wildcard})
		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{kind: cloudflare.Kind}, DNS: writer, StackState: prior}

		state, err := settleStackRecords(t.Context(), cfg, []edge.StackSpec{{}}, prior, func(string) {})
		if err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(writer.deleted) != 1 || writer.deleted[0] != wildcard {
			t.Errorf("deleted = %v, want %v", writer.deleted, wildcard)
		}
		if state.Records != nil {
			t.Errorf("state = %+v, want no records recorded", state)
		}
	})
}

func TestReleaseRecords(t *testing.T) {
	t.Parallel()

	wildcard := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
	var state edge.StackState
	state.RecordWrites([]edge.Record{wildcard})

	t.Run("teardown deletes every record the stack wrote", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		if err := releaseRecords(t.Context(), writer, state, func(string) {}); err != nil {
			t.Fatalf("releaseRecords: %v", err)
		}
		if len(writer.deleted) != 1 || writer.deleted[0] != wildcard {
			t.Errorf("deleted = %v, want %v", writer.deleted, wildcard)
		}
	})

	t.Run("teardown without a writer says what it leaves behind", func(t *testing.T) {
		t.Parallel()

		var said []string
		if err := releaseRecords(t.Context(), nil, state, func(m string) { said = append(said, m) }); err != nil {
			t.Fatalf("releaseRecords: %v", err)
		}
		if len(said) != 1 || !strings.Contains(said[0], "shop.app.com") {
			t.Errorf("said = %v, want the abandoned record named", said)
		}
	})

	t.Run("a stack that wrote nothing says nothing", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		if err := releaseRecords(t.Context(), writer, edge.StackState{}, func(string) {
			t.Error("said something about a stack that wrote no records")
		}); err != nil {
			t.Fatalf("releaseRecords: %v", err)
		}
		if len(writer.deleted) != 0 {
			t.Errorf("deleted = %v, want none", writer.deleted)
		}
	})
}
