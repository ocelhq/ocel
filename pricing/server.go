package pricing

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/costkit"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
)

const TokensVariable = "PRICING_TOKENS"

type Options struct {
	Tokens []string
	Logger *slog.Logger
}

func Handler(store costkit.Store, opts Options) http.Handler {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	path, handler := costv1connect.NewCostServiceHandler(
		&service{store: store, table: Table(), notes: Notes()},
		connect.WithInterceptors(validate.NewInterceptor()),
	)
	if len(opts.Tokens) == 0 {
		log.Warn("every caller may price against this listener", "variable", TokensVariable)
	} else {
		handler = allowed(handler, opts.Tokens)
	}
	mux.Handle(path, handler)
	return mux
}

func Commas(raw string) []string {
	var held []string
	for _, item := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			held = append(held, trimmed)
		}
	}
	return held
}

func allowed(next http.Handler, tokens []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !bears(r.Header.Get("Authorization"), tokens) {
			http.Error(w, "this pricer answers callers bearing a token it was started with", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bears(header string, tokens []string) bool {
	presented, carried := strings.CutPrefix(header, "Bearer ")
	matched := 0
	for _, token := range tokens {
		matched |= subtle.ConstantTimeCompare([]byte(presented), []byte(token))
	}
	return carried && matched == 1
}

type service struct {
	store costkit.Store
	table costkit.Table
	notes []string
}

func (s *service) Price(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	estimate, err := costkit.Estimate(s.store, s.table, req)
	var usage *costkit.UsageError
	if errors.As(err, &usage) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	estimate.Notes = append(estimate.Notes, s.notes...)
	return estimate, nil
}
