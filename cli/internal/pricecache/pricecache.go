package pricecache

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/pkg/pricing"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

const (
	pricedTTL     = 24 * time.Hour
	missTTL       = time.Hour
	maxEntries    = 10000
	maxBatch      = 500
	maxAttempts   = 5
	firstBackoff  = 500 * time.Millisecond
	maxBackoff    = 10 * time.Second
	maxRetryAfter = time.Minute
)

const unavailableReason = "--offline asks the console for none"

type Catalog struct {
	path    string
	baseURL string
	client  *console.Client
	token   string

	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	oldest time.Time
}

func Path() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find the cache directory for prices: %w", err)
	}
	return filepath.Join(base, "ocel", "prices", "answers.json"), nil
}

func New(path, baseURL, token string) *Catalog {
	return &Catalog{path: path, baseURL: baseURL, client: console.New(baseURL), token: token, now: time.Now, sleep: sleep}
}

func NewOffline(path, baseURL string) *Catalog {
	return &Catalog{path: path, baseURL: baseURL, now: time.Now, sleep: sleep}
}

func (c *Catalog) OldestCached() time.Time {
	return c.oldest
}

func (c *Catalog) Lookup(ctx context.Context, queries []pricing.Query) ([]pricing.Answer, error) {
	cache := readCache(c.path)
	now := c.now()
	byKey := make(map[string]pricing.Answer, len(queries))
	queued := make(map[string]bool)
	var pending []pricing.Query
	for _, query := range queries {
		key := query.Key()
		if _, seen := byKey[key]; seen || queued[key] {
			continue
		}
		hit, ok := cache[entry{Console: c.baseURL, Key: key}.slot()]
		if ok && (c.client == nil || hit.fresh(now)) {
			byKey[key] = hit.answer
			if c.oldest.IsZero() || hit.FetchedAt.Before(c.oldest) {
				c.oldest = hit.FetchedAt
			}
			continue
		}
		if c.client == nil {
			byKey[key] = pricing.Answer{Kind: pricing.AnswerUnavailable, Reason: unavailableReason}
			continue
		}
		queued[key] = true
		pending = append(pending, query)
	}

	if len(pending) > 0 {
		received, err := c.fetch(ctx, pending)
		if err != nil {
			return nil, err
		}
		for i, query := range pending {
			key := query.Key()
			raw, err := protojson.Marshal(received[i])
			if err != nil {
				return nil, err
			}
			fresh, err := newCached(entry{Console: c.baseURL, Key: key, FetchedAt: now, Answer: raw}, received[i])
			if err != nil {
				return nil, fmt.Errorf("the price catalog at %s answered a price this CLI cannot read: %w", c.baseURL, err)
			}
			byKey[key] = fresh.answer
			cache[fresh.slot()] = fresh
		}
		writeCache(c.path, cache)
	}

	answers := make([]pricing.Answer, len(queries))
	for i, query := range queries {
		answers[i] = byKey[query.Key()]
	}
	return answers, nil
}

func (c *Catalog) fetch(ctx context.Context, queries []pricing.Query) ([]*consolev1.PriceAnswer, error) {
	received := make([]*consolev1.PriceAnswer, 0, len(queries))
	for batch := range slices.Chunk(queries, maxBatch) {
		wire := make([]*consolev1.PriceQuery, 0, len(batch))
		for _, query := range batch {
			converted, err := wireQuery(query)
			if err != nil {
				return nil, err
			}
			wire = append(wire, converted)
		}
		resp, err := c.lookup(ctx, wire)
		if err != nil {
			return nil, fmt.Errorf("the price catalog at %s did not answer (%w); retry, or pass --offline to use cached prices", c.baseURL, err)
		}
		if len(resp.GetAnswers()) != len(batch) {
			return nil, fmt.Errorf("the price catalog at %s answered %d of %d price queries", c.baseURL, len(resp.GetAnswers()), len(batch))
		}
		received = append(received, resp.GetAnswers()...)
	}
	return received, nil
}

func (c *Catalog) lookup(ctx context.Context, queries []*consolev1.PriceQuery) (*consolev1.LookupResponse, error) {
	for n := 1; ; n++ {
		resp, err := c.client.LookupPrices(ctx, c.token, queries)
		if err == nil {
			return resp, nil
		}
		if c.token != "" && connect.CodeOf(err) == connect.CodeUnauthenticated {
			c.token = ""
			continue
		}
		if !retryable(err) || ctx.Err() != nil || n == maxAttempts {
			return nil, err
		}
		if err := c.sleep(ctx, backoff(n, retryAfter(err))); err != nil {
			return nil, err
		}
	}
}

func retryable(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeResourceExhausted, connect.CodeDeadlineExceeded,
		connect.CodeInternal, connect.CodeUnknown:
		return true
	}
	return false
}

func retryAfter(err error) time.Duration {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return 0
	}
	header := strings.TrimSpace(connectErr.Meta().Get("Retry-After"))
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil {
		return max(time.Until(at), 0)
	}
	return 0
}

func backoff(attempt int, asked time.Duration) time.Duration {
	if asked > 0 {
		return min(asked, maxRetryAfter)
	}
	ceiling := min(firstBackoff<<(attempt-1), maxBackoff)
	return ceiling/2 + rand.N(ceiling/2)
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func wireQuery(query pricing.Query) (*consolev1.PriceQuery, error) {
	if option := cmp.Or(query.PurchaseOption, pricing.PurchaseOnDemand); option != pricing.PurchaseOnDemand {
		return nil, fmt.Errorf("the price catalog answers on-demand prices only, not %q", option)
	}
	return &consolev1.PriceQuery{
		Vendor:         query.Vendor,
		Service:        query.Service,
		Region:         query.Region,
		ProductFamily:  query.ProductFamily,
		Attributes:     query.Attributes,
		PurchaseOption: consolev1.PurchaseOption_PURCHASE_OPTION_ON_DEMAND,
		Unit:           query.Unit,
	}, nil
}

func readAnswer(answer *consolev1.PriceAnswer) (pricing.Answer, error) {
	switch {
	case answer.GetPrice() != nil:
		price := answer.GetPrice()
		steps, err := readSteps(price.GetTiers())
		if err != nil {
			return pricing.Answer{}, fmt.Errorf("sku %s: %w", price.GetSku(), err)
		}
		return pricing.Answer{Kind: pricing.AnswerPriced, Price: pricing.Price{
			SKU:            price.GetSku(),
			Unit:           price.GetUnit(),
			Steps:          steps,
			CatalogVersion: price.GetCatalogVersion(),
			Source:         price.GetSourceUrl(),
		}}, nil
	case answer.GetAmbiguous() != nil:
		ambiguous := answer.GetAmbiguous()
		return pricing.Answer{
			Kind:       pricing.AnswerAmbiguous,
			Candidates: ambiguous.GetCandidateSkus(),
			Matches:    int(ambiguous.GetMatches()),
		}, nil
	case answer.GetMiss() != nil:
		return pricing.Answer{Kind: pricing.AnswerMissing, Reason: missReason(answer.GetMiss())}, nil
	}
	return pricing.Answer{}, errors.New("an answer that is neither a price, a miss nor an ambiguity")
}

func missReason(miss *consolev1.PriceMiss) string {
	if detail := strings.TrimSpace(miss.GetDetail()); detail != "" {
		return detail
	}
	if miss.GetReason() == consolev1.PriceMiss_REASON_CATALOG_NOT_SEEDED {
		return "the console has no catalog for this service"
	}
	return "no price in the catalog matches"
}

func readSteps(tiers []*consolev1.PriceTier) ([]pricing.Step, error) {
	if len(tiers) == 0 {
		return nil, errors.New("a price with no tiers")
	}
	steps := make([]pricing.Step, 0, len(tiers))
	for _, tier := range tiers {
		start, err := decimal.NewFromString(tier.GetStart())
		if err != nil {
			return nil, fmt.Errorf("tier start %q is not a decimal", tier.GetStart())
		}
		price, err := decimal.NewFromString(tier.GetPrice())
		if err != nil {
			return nil, fmt.Errorf("tier price %q is not a decimal", tier.GetPrice())
		}
		steps = append(steps, pricing.Step{Start: start, Price: price})
	}
	return steps, nil
}
