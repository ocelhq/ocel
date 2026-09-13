package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/ocelhq/ocel/platform/gcp/provider/cost/catalog"
	"github.com/ocelhq/ocel/pricing"
	"github.com/ocelhq/ocel/pricing/ingest"
	"github.com/ocelhq/ocel/pricing/store/postgres"
)

func main() {
	services := flag.String("service", "", "the vendor services to ingest, comma separated; the cards' own queries when empty")
	regions := flag.String("region", "", "the AWS regions to ingest, comma separated; every region a card rate names when empty")
	vendor := flag.String("vendor", "", "aws or gcp; both when empty")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log, list(*services), list(*regions), *vendor); err != nil {
		log.Error("the ingest stopped", "error", err.Error())
		os.Exit(1)
	}
}

func list(raw string) []string {
	var held []string
	for _, item := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			held = append(held, trimmed)
		}
	}
	return held
}

func run(log *slog.Logger, services, regions []string, vendor string) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return fmt.Errorf("DATABASE_URL names no rate store to ingest into")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	card, err := pricing.Cards()
	if err != nil {
		return err
	}
	store, err := postgres.Open(ctx, url, card)
	if err != nil {
		return err
	}
	defer store.Close()

	if vendor == "" || vendor == postgres.VendorAWS {
		loader := ingest.AWS{Store: store}
		for _, target := range ingest.AWSTargets(card) {
			if len(services) > 0 && !slices.Contains(services, target.Service) {
				continue
			}
			if len(regions) > 0 && !slices.Contains(regions, target.Region) {
				continue
			}
			log.Info("ingesting an offer file", "vendor", postgres.VendorAWS, "service", target.Service, "region", target.Region)
			if err := loader.Run(ctx, target.Service, target.Region); err != nil {
				return err
			}
		}
	}

	if vendor == postgres.VendorAWS {
		return nil
	}
	key := os.Getenv(catalog.KeyVariable)
	if key == "" {
		log.Warn("the Cloud Billing catalog answers registered callers only, so GCP prices stay on the embedded card", "variable", catalog.KeyVariable)
		return nil
	}
	loader := ingest.GCP{Store: store, Key: key}
	for _, service := range ingest.GCPServices(card) {
		if len(services) > 0 && !slices.Contains(services, service) {
			continue
		}
		log.Info("ingesting a billing catalog", "vendor", postgres.VendorGCP, "service", service)
		if err := loader.Run(ctx, service); err != nil {
			return err
		}
	}
	return nil
}
