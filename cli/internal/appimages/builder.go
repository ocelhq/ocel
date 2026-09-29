package appimages

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/runui"
)

func RequireBuilder(ctx context.Context, scope *events.Scope, cfg *projectconfig.Config, archs map[string]string) error {
	var chosen []image.Recipe
	for _, app := range Apps(cfg) {
		described, err := image.Describe(cfg, app)
		if err != nil {
			return err
		}
		recipe, err := image.ChooseRecipe(described)
		if err != nil {
			return err
		}
		chosen = append(chosen, recipe)
	}
	if len(chosen) == 0 {
		return nil
	}
	containers := make([]string, len(chosen))
	for i, recipe := range chosen {
		containers[i] = recipe.App.Name
		if notice := recipe.Notice(); notice != "" {
			scope.Say(notice)
		}
	}
	if err := image.RefuseUnusableDaemon(ctx, slices.Sorted(maps.Values(archs))...); err != nil {
		return fmt.Errorf("building the container image for %s happens on this machine, before anything is provisioned:\n    %w", runui.Quoted(containers), err)
	}
	return nil
}
