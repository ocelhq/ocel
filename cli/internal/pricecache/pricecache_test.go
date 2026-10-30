package pricecache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/pricecache/pricecachetest"
	"github.com/ocelhq/ocel/pkg/pricing"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

type fixedCatalog map[string]pricing.Answer

func (f fixedCatalog) Lookup(_ context.Context, queries []pricing.Query) ([]pricing.Answer, error) {
	answers := make([]pricing.Answer, len(queries))
	for i, query := range queries {
		answer, ok := f[query.Service]
		if !ok {
			answer = pricing.Answer{Kind: pricing.AnswerMissing, Reason: "no product matches"}
		}
		answers[i] = answer
	}
	return answers, nil
}

func priced(sku string, price string) pricing.Answer {
	return pricing.Answer{Kind: pricing.AnswerPriced, Price: pricing.Price{
		SKU:            sku,
		Unit:           "Request",
		Steps:          []pricing.Step{{Start: decimal.Zero, Price: decimal.RequireFromString(price)}},
		CatalogVersion: "20261022183959",
		Source:         "https://prices.example.test/" + sku,
	}}
}

var catalog = fixedCatalog{
	"Requests": priced("SKU-REQUESTS", "0.0000002"),
	"Storage":  priced("SKU-STORAGE", "0.023"),
	"Logs":     {Kind: pricing.AnswerAmbiguous, Candidates: []string{"SKU-A", "SKU-B"}, Matches: 3},
}

func query(service string) pricing.Query {
	return pricing.Query{Vendor: "fake", Service: service, Region: "region-1", Attributes: map[string]string{"usage": service}}
}

type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func newCatalog(t *testing.T, path, url, token string, at *clock) *Catalog {
	t.Helper()
	c := New(path, url, token)
	c.now = at.now
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func cachePath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "prices", "answers.json")
}

func TestALookupSendsEachDistinctQueryOnceAndAnswersEveryQueryInOrder(t *testing.T) {
	server := pricecachetest.Serve(t, catalog)
	c := newCatalog(t, cachePath(t), server.URL, "", &clock{time.Now()})

	answers, err := c.Lookup(context.Background(), []pricing.Query{query("Requests"), query("Storage"), query("Requests"), query("Logs"), query("Queues")})
	if err != nil {
		t.Fatalf("Lookup() = %v", err)
	}

	calls := server.Calls()
	if len(calls) != 1 || len(calls[0]) != 4 {
		t.Fatalf("calls = %v, want one call with the four distinct queries", calls)
	}
	if got := answers[0].Price.SKU + " " + answers[1].Price.SKU + " " + answers[2].Price.SKU; got != "SKU-REQUESTS SKU-STORAGE SKU-REQUESTS" {
		t.Errorf("priced skus = %q, want each answer in the order its query was asked", got)
	}
	if !answers[0].Price.Steps[0].Price.Equal(decimal.RequireFromString("0.0000002")) || answers[0].Price.CatalogVersion != "20261022183959" || answers[0].Price.Source != "https://prices.example.test/SKU-REQUESTS" {
		t.Errorf("answer = %+v, want the price, catalog version and source the console answered", answers[0])
	}
	if answers[3].Kind != pricing.AnswerAmbiguous || strings.Join(answers[3].Candidates, ",") != "SKU-A,SKU-B" || answers[3].Matches != 3 {
		t.Errorf("ambiguous answer = %+v, want both candidate skus of the three matches", answers[3])
	}
	if answers[4].Kind != pricing.AnswerMissing || answers[4].Reason != "no product matches" {
		t.Errorf("missing answer = %+v, want the console's miss detail", answers[4])
	}
}

func TestALookupSplitsMoreThanFiveHundredQueriesIntoSeveralCalls(t *testing.T) {
	server := pricecachetest.Serve(t, catalog)
	c := newCatalog(t, cachePath(t), server.URL, "", &clock{time.Now()})
	queries := make([]pricing.Query, 0, 1201)
	for i := range 1201 {
		queries = append(queries, query(fmt.Sprintf("Service%d", i)))
	}

	answers, err := c.Lookup(context.Background(), queries)
	if err != nil {
		t.Fatalf("Lookup() = %v", err)
	}

	calls := server.Calls()
	if len(calls) != 3 || len(calls[0]) != 500 || len(calls[1]) != 500 || len(calls[2]) != 201 {
		t.Errorf("calls = %d, want 500, 500 and 201 queries", len(calls))
	}
	if len(answers) != 1201 {
		t.Errorf("answers = %d, want one per query", len(answers))
	}
}

func TestALookupAsksAnonymouslyWithoutATokenAndWithTheBearerWithOne(t *testing.T) {
	server := pricecachetest.Serve(t, catalog)

	if _, err := newCatalog(t, cachePath(t), server.URL, "", &clock{time.Now()}).Lookup(context.Background(), []pricing.Query{query("Requests")}); err != nil {
		t.Fatal(err)
	}
	if _, err := newCatalog(t, cachePath(t), server.URL, "session-token", &clock{time.Now()}).Lookup(context.Background(), []pricing.Query{query("Requests")}); err != nil {
		t.Fatal(err)
	}

	if got := server.Authorization(); len(got) != 2 || got[0] != "" || got[1] != "Bearer session-token" {
		t.Errorf("Authorization = %q, want none and then the session bearer", got)
	}
}

func TestACachedAnswerIsServedUntilItsTimeToLiveRunsOut(t *testing.T) {
	server := pricecachetest.Serve(t, catalog)
	path := cachePath(t)
	at := &clock{time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	if _, err := newCatalog(t, path, server.URL, "", at).Lookup(context.Background(), []pricing.Query{query("Requests"), query("Queues")}); err != nil {
		t.Fatal(err)
	}

	at.at = at.at.Add(30 * time.Minute)
	if _, err := newCatalog(t, path, server.URL, "", at).Lookup(context.Background(), []pricing.Query{query("Requests"), query("Queues")}); err != nil {
		t.Fatal(err)
	}
	if calls := server.Calls(); len(calls) != 1 {
		t.Errorf("calls after 30 minutes = %d, want both answers served from the cache", len(calls))
	}

	at.at = at.at.Add(time.Hour)
	if _, err := newCatalog(t, path, server.URL, "", at).Lookup(context.Background(), []pricing.Query{query("Requests"), query("Queues")}); err != nil {
		t.Fatal(err)
	}
	if calls := server.Calls(); len(calls) != 2 || len(calls[1]) != 1 || calls[1][0].GetService() != "Queues" {
		t.Errorf("calls after 90 minutes = %v, want only the miss asked again", calls)
	}

	at.at = at.at.Add(24 * time.Hour)
	if _, err := newCatalog(t, path, server.URL, "", at).Lookup(context.Background(), []pricing.Query{query("Requests")}); err != nil {
		t.Fatal(err)
	}
	if calls := server.Calls(); len(calls) != 3 {
		t.Errorf("calls after a day = %d, want the price asked again", len(calls))
	}
}

func TestAnOfflineLookupNeverCallsAndAnswersUncachedQueriesAsUnavailable(t *testing.T) {
	server := pricecachetest.Serve(t, catalog)
	path := cachePath(t)
	fetched := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	if _, err := newCatalog(t, path, server.URL, "", &clock{fetched}).Lookup(context.Background(), []pricing.Query{query("Requests")}); err != nil {
		t.Fatal(err)
	}

	offline := NewOffline(path, server.URL)
	offline.now = (&clock{fetched.Add(72 * time.Hour)}).now
	answers, err := offline.Lookup(context.Background(), []pricing.Query{query("Requests"), query("Storage")})
	if err != nil {
		t.Fatalf("Lookup() = %v", err)
	}

	if len(server.Calls()) != 1 {
		t.Errorf("calls = %d, want the offline lookup to call nothing", len(server.Calls()))
	}
	if answers[0].Kind != pricing.AnswerPriced || answers[0].Price.SKU != "SKU-REQUESTS" {
		t.Errorf("cached answer = %+v, want the three-day-old price served", answers[0])
	}
	if answers[1].Kind != pricing.AnswerUnavailable {
		t.Errorf("uncached answer = %+v, want it unavailable", answers[1])
	}
	if !offline.OldestCached().Equal(fetched) {
		t.Errorf("OldestCached() = %v, want %v", offline.OldestCached(), fetched)
	}
}

func TestALookupRetriesAThrottleAndAnOutageAndGivesUpOnARefusal(t *testing.T) {
	t.Run("a throttle is retried after the delay it asks for", func(t *testing.T) {
		server := pricecachetest.Serve(t, catalog)
		throttle := connect.NewError(connect.CodeResourceExhausted, errors.New("slow down"))
		throttle.Meta().Set("Retry-After", "7")
		server.RefuseNext(throttle, connect.NewError(connect.CodeUnavailable, errors.New("down")))
		c := newCatalog(t, cachePath(t), server.URL, "", &clock{time.Now()})
		var slept []time.Duration
		c.sleep = func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		}

		answers, err := c.Lookup(context.Background(), []pricing.Query{query("Requests")})
		if err != nil {
			t.Fatalf("Lookup() = %v", err)
		}
		if answers[0].Kind != pricing.AnswerPriced || len(server.Calls()) != 3 {
			t.Errorf("answers = %+v after %d calls, want the third call to price it", answers, len(server.Calls()))
		}
		if len(slept) != 2 || slept[0] != 7*time.Second || slept[1] <= 0 || slept[1] > 2*firstBackoff {
			t.Errorf("slept = %v, want the asked 7s and then a jittered backoff", slept)
		}
	})

	t.Run("a refusal is not retried", func(t *testing.T) {
		server := pricecachetest.Serve(t, catalog)
		server.RefuseNext(connect.NewError(connect.CodeInvalidArgument, errors.New("bad query")))
		c := newCatalog(t, cachePath(t), server.URL, "", &clock{time.Now()})

		_, err := c.Lookup(context.Background(), []pricing.Query{query("Requests")})
		if err == nil || len(server.Calls()) != 1 {
			t.Errorf("err = %v after %d calls, want one call and its refusal", err, len(server.Calls()))
		}
	})

	t.Run("an outage that outlasts every attempt names the console and the offline flag", func(t *testing.T) {
		server := pricecachetest.Serve(t, catalog)
		for range maxAttempts {
			server.RefuseNext(connect.NewError(connect.CodeUnavailable, errors.New("down")))
		}
		c := newCatalog(t, cachePath(t), server.URL, "", &clock{time.Now()})

		_, err := c.Lookup(context.Background(), []pricing.Query{query("Requests")})
		if err == nil || !strings.Contains(err.Error(), server.URL) || !strings.Contains(err.Error(), "--offline") {
			t.Errorf("err = %v, want it to name %s and --offline", err, server.URL)
		}
		if len(server.Calls()) != maxAttempts {
			t.Errorf("calls = %d, want %d attempts", len(server.Calls()), maxAttempts)
		}
	})
}

func TestTheCacheKeepsTheNewestEntriesUpToItsBound(t *testing.T) {
	path := cachePath(t)
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	miss := &consolev1.PriceAnswer{Outcome: &consolev1.PriceAnswer_Miss{Miss: &consolev1.PriceMiss{Reason: consolev1.PriceMiss_REASON_NO_MATCH, Detail: "none"}}}
	raw, err := protojson.Marshal(miss)
	if err != nil {
		t.Fatal(err)
	}
	cache := make(map[string]cached, maxEntries+5)
	for i := range maxEntries + 5 {
		key := fmt.Sprintf("key-%05d", i)
		cache[key] = cached{entry: entry{Key: key, FetchedAt: at.Add(time.Duration(i) * time.Second), Answer: raw}}
	}

	writeCache(path, cache)

	kept := readCache(path)
	if len(kept) != maxEntries {
		t.Fatalf("kept = %d, want %d", len(kept), maxEntries)
	}
	for i := range 5 {
		if _, ok := kept[entry{Key: fmt.Sprintf("key-%05d", i)}.slot()]; ok {
			t.Errorf("key-%05d kept, want the oldest entries evicted first", i)
		}
	}
}

func TestAnUnreadableCacheIsTreatedAsEmpty(t *testing.T) {
	path := cachePath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := pricecachetest.Serve(t, catalog)

	answers, err := newCatalog(t, path, server.URL, "", &clock{time.Now()}).Lookup(context.Background(), []pricing.Query{query("Requests")})
	if err != nil || answers[0].Kind != pricing.AnswerPriced {
		t.Errorf("Lookup() = %+v, %v, want the price fetched past the unreadable cache", answers, err)
	}
	if len(readCache(path)) != 1 {
		t.Errorf("the cache was not rewritten with the fetched answer")
	}
}

func TestALookupAsksAnonymouslyOnceTheConsoleRefusesItsSession(t *testing.T) {
	server := pricecachetest.Serve(t, catalog)
	server.RefuseNext(connect.NewError(connect.CodeUnauthenticated, errors.New("session expired")))
	c := newCatalog(t, cachePath(t), server.URL, "stale-token", &clock{time.Now()})

	answers, err := c.Lookup(context.Background(), []pricing.Query{query("Requests")})
	if err != nil || answers[0].Kind != pricing.AnswerPriced {
		t.Fatalf("Lookup() = %+v, %v, want the price asked for anonymously", answers, err)
	}
	if got := server.Authorization(); len(got) != 2 || got[0] != "Bearer stale-token" || got[1] != "" {
		t.Errorf("Authorization = %q, want the session and then none", got)
	}
}

func TestTheCacheHoldsTheConsolesAnswerAsReceived(t *testing.T) {
	server := pricecachetest.Serve(t, catalog)
	path := cachePath(t)
	if _, err := newCatalog(t, path, server.URL, "", &clock{time.Now()}).Lookup(context.Background(), []pricing.Query{query("Requests")}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Entries []struct {
			Answer json.RawMessage `json:"answer"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &file); err != nil || len(file.Entries) != 1 {
		t.Fatalf("cache = %s (%v), want one entry", raw, err)
	}
	var answer consolev1.PriceAnswer
	if err := protojson.Unmarshal(file.Entries[0].Answer, &answer); err != nil {
		t.Fatalf("the cached answer is not the console's PriceAnswer: %v", err)
	}
	if answer.GetPrice().GetSku() != "SKU-REQUESTS" || answer.GetPrice().GetTiers()[0].GetPrice() != "0.0000002" {
		t.Errorf("cached answer = %v, want the price the console sent", &answer)
	}
}

func TestACachedAnswerThisCLICannotReadIsAskedForAgain(t *testing.T) {
	server := pricecachetest.Serve(t, catalog)
	path := cachePath(t)
	at := &clock{time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	unreadable := &consolev1.PriceAnswer{Outcome: &consolev1.PriceAnswer_Price{Price: &consolev1.Price{Sku: "SKU-REQUESTS", Unit: "Request", Tiers: []*consolev1.PriceTier{{Start: "0", Price: "cheap"}}}}}
	raw, err := protojson.Marshal(unreadable)
	if err != nil {
		t.Fatal(err)
	}
	file, err := json.Marshal(cacheFile{Entries: []entry{{Console: server.URL, Key: query("Requests").Key(), FetchedAt: at.at, Answer: raw}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}

	answers, err := newCatalog(t, path, server.URL, "", at).Lookup(context.Background(), []pricing.Query{query("Requests")})
	if err != nil {
		t.Fatalf("Lookup() = %v", err)
	}

	if len(server.Calls()) != 1 || answers[0].Kind != pricing.AnswerPriced || !answers[0].Price.Steps[0].Price.Equal(decimal.RequireFromString("0.0000002")) {
		t.Errorf("answers = %+v after %d calls, want the unreadable entry dropped and the price asked for again", answers, len(server.Calls()))
	}
}

func TestALookupNeverServesAnotherConsolesCachedAnswer(t *testing.T) {
	first := pricecachetest.Serve(t, catalog)
	second := pricecachetest.Serve(t, fixedCatalog{"Requests": priced("SKU-SECOND", "0.1")})
	path := cachePath(t)
	at := &clock{time.Now()}
	lookup := func(c *Catalog) pricing.Answer {
		t.Helper()
		answers, err := c.Lookup(context.Background(), []pricing.Query{query("Requests")})
		if err != nil {
			t.Fatalf("Lookup() = %v", err)
		}
		return answers[0]
	}

	lookup(newCatalog(t, path, first.URL, "", at))
	if got := lookup(newCatalog(t, path, second.URL, "", at)); got.Price.SKU != "SKU-SECOND" || len(second.Calls()) != 1 {
		t.Errorf("second console answer = %+v after %d calls, want its own price asked for", got, len(second.Calls()))
	}
	if got := lookup(newCatalog(t, path, first.URL, "", at)); got.Price.SKU != "SKU-REQUESTS" || len(first.Calls()) != 1 {
		t.Errorf("first console answer = %+v after %d calls, want its cached price kept beside the second's", got, len(first.Calls()))
	}
	if got := lookup(NewOffline(path, "https://elsewhere.example.test")); got.Kind != pricing.AnswerUnavailable {
		t.Errorf("offline answer for a console never asked = %+v, want it unavailable", got)
	}
	if got := lookup(NewOffline(path, second.URL)); got.Price.SKU != "SKU-SECOND" {
		t.Errorf("offline answer for the second console = %+v, want its cached price", got)
	}
}
