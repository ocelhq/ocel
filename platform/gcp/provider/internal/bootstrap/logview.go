package bootstrap

import (
	"context"
	"fmt"
	"net/http"

	"google.golang.org/api/logging/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const (
	reasonLogViewFilter = "its filter drifted"
	logViewFilterField  = "filter"
)

func TierLogFilter(tier environment.Tier) string {
	return fmt.Sprintf(`resource.type="cloud_run_revision" AND labels.%q=%q`, names.TierLabel, string(tier))
}

var logViewKind = KindOps{
	noun:          "log view",
	terraform:     cost.LogViewType,
	emulatorSkips: true,
	survey: func(ctx context.Context, b Engine, read Survey, _ Resource) (Presence, error) {
		view, err := b.readLogView(ctx, read.Tier)
		if err != nil || view == nil {
			return Presence{}, err
		}
		if view.Filter != TierLogFilter(read.Tier) {
			return Presence{Present: true, Mends: reasonLogViewFilter}, nil
		}
		return Presence{Present: true}, nil
	},
	Make: func(ctx context.Context, b Engine, read Survey, _ Resource) error {
		return b.makeLogView(ctx, read.Tier)
	},
	mend: func(ctx context.Context, b Engine, read Survey, _ Resource) error {
		return b.filterLogView(ctx, read.Tier)
	},
	Take: func(ctx context.Context, b Engine, read Survey, _ Resource) error {
		return b.takeLogView(ctx, read.Tier)
	},
}

func (b Engine) readLogView(ctx context.Context, tier environment.Tier) (*logging.LogView, error) {
	service, err := b.Clients.LoggingREST()
	if err != nil {
		return nil, err
	}
	view, err := googleretry.Attempted(ctx, service.Projects.Locations.Buckets.Views.Get(b.Clients.LogViewPath(tier)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the %s log view: %w", b.Clients.LogView(tier), err)
	}
	return view, nil
}

func (b Engine) makeLogView(ctx context.Context, tier environment.Tier) error {
	service, err := b.Clients.LoggingREST()
	if err != nil {
		return err
	}
	view := &logging.LogView{
		Description: "the Cloud Run entries of ocel's " + string(tier) + " tier, the only logs its deploy credential may read",
		Filter:      TierLogFilter(tier),
	}
	_, err = googleretry.Attempted(ctx, service.Projects.Locations.Buckets.Views.Create(b.Clients.LogBucketPath(), view).
		ViewId(b.Clients.LogView(tier)).Context(ctx).Do)
	if googleretry.AnsweredCode(err) == http.StatusConflict {
		return b.filterLogView(ctx, tier)
	}
	if err != nil {
		return fmt.Errorf("create the %s log view: %w", b.Clients.LogView(tier), err)
	}
	return nil
}

func (b Engine) filterLogView(ctx context.Context, tier environment.Tier) error {
	service, err := b.Clients.LoggingREST()
	if err != nil {
		return err
	}
	_, err = googleretry.Attempted(ctx, service.Projects.Locations.Buckets.Views.Patch(b.Clients.LogViewPath(tier),
		&logging.LogView{Filter: TierLogFilter(tier)}).UpdateMask(logViewFilterField).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("filter the %s log view to the %s tier's entries: %w", b.Clients.LogView(tier), tier, err)
	}
	return nil
}

func (b Engine) takeLogView(ctx context.Context, tier environment.Tier) error {
	service, err := b.Clients.LoggingREST()
	if err != nil {
		return err
	}
	_, err = googleretry.Attempted(ctx, service.Projects.Locations.Buckets.Views.Delete(b.Clients.LogViewPath(tier)).Context(ctx).Do)
	if err != nil && !gcloud.Absent(err) {
		return fmt.Errorf("delete the %s log view: %w", b.Clients.LogView(tier), err)
	}
	return nil
}
