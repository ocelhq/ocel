package pricecachetest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/pricing"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

type Console struct {
	URL string

	mu      sync.Mutex
	catalog pricing.Catalog
	calls   [][]*consolev1.PriceQuery
	auth    []string
	refuse  []error
}

func New(catalog pricing.Catalog) *Console {
	return &Console{catalog: catalog}
}

func Serve(t testing.TB, catalog pricing.Catalog) *Console {
	t.Helper()
	c := New(catalog)
	mux := http.NewServeMux()
	c.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c.URL = srv.URL
	return c
}

func (c *Console) Mount(mux *http.ServeMux) {
	path, handler := consolev1connect.NewPricingServiceHandler(c, connect.WithInterceptors(connectvalidate.NewInterceptor()))
	mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.auth = append(c.auth, r.Header.Get("Authorization"))
		c.mu.Unlock()
		handler.ServeHTTP(w, r)
	})))
}

func (c *Console) Answer(catalog pricing.Catalog) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.catalog = catalog
}

func (c *Console) RefuseNext(errs ...error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refuse = append(c.refuse, errs...)
}

func (c *Console) Calls() [][]*consolev1.PriceQuery {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]*consolev1.PriceQuery(nil), c.calls...)
}

func (c *Console) Authorization() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.auth...)
}

func (c *Console) Lookup(ctx context.Context, req *consolev1.LookupRequest) (*consolev1.LookupResponse, error) {
	c.mu.Lock()
	c.calls = append(c.calls, req.GetQueries())
	var refusal error
	if len(c.refuse) > 0 {
		refusal, c.refuse = c.refuse[0], c.refuse[1:]
	}
	catalog := c.catalog
	c.mu.Unlock()
	if refusal != nil {
		return nil, refusal
	}
	if catalog == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this console answers no price queries"))
	}
	queries := make([]pricing.Query, 0, len(req.GetQueries()))
	for _, query := range req.GetQueries() {
		queries = append(queries, pricing.Query{
			Vendor:         query.GetVendor(),
			Service:        query.GetService(),
			Region:         query.GetRegion(),
			ProductFamily:  query.GetProductFamily(),
			Attributes:     query.GetAttributes(),
			PurchaseOption: pricing.PurchaseOnDemand,
			Unit:           query.GetUnit(),
		})
	}
	answers, err := catalog.Lookup(ctx, queries)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if len(answers) != len(queries) {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the catalog answered a different number of queries than it was asked"))
	}
	resp := &consolev1.LookupResponse{Currency: "USD"}
	for _, answer := range answers {
		resp.Answers = append(resp.Answers, wireAnswer(answer))
	}
	return resp, nil
}

func wireAnswer(answer pricing.Answer) *consolev1.PriceAnswer {
	switch answer.Kind {
	case pricing.AnswerPriced:
		tiers := make([]*consolev1.PriceTier, 0, len(answer.Price.Steps))
		for _, step := range answer.Price.Steps {
			tiers = append(tiers, &consolev1.PriceTier{Start: step.Start.String(), Price: step.Price.String()})
		}
		return &consolev1.PriceAnswer{Outcome: &consolev1.PriceAnswer_Price{Price: &consolev1.Price{
			Sku:            answer.Price.SKU,
			Unit:           answer.Price.Unit,
			Tiers:          tiers,
			CatalogVersion: answer.Price.CatalogVersion,
			SourceUrl:      answer.Price.Source,
		}}}
	case pricing.AnswerAmbiguous:
		return &consolev1.PriceAnswer{Outcome: &consolev1.PriceAnswer_Ambiguous{Ambiguous: &consolev1.PriceAmbiguity{
			CandidateSkus: answer.Candidates,
			Matches:       uint32(max(answer.Matches, len(answer.Candidates))),
		}}}
	default:
		return &consolev1.PriceAnswer{Outcome: &consolev1.PriceAnswer_Miss{Miss: &consolev1.PriceMiss{
			Reason: consolev1.PriceMiss_REASON_NO_MATCH,
			Detail: answer.Reason,
		}}}
	}
}
