package providerserver

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func usesOf(root string, manifest *contractv1.Manifest) ([]buildoutput.Uses, error) {
	var apps []buildoutput.Uses
	for _, app := range manifest.GetApps() {
		if app.GetContainer() != nil {
			continue
		}
		hosting, present, err := buildoutput.ReadHosting(root, app.GetName())
		if err != nil {
			return nil, err
		}
		uses, err := buildUses(root, app.GetName(), app.GetFramework().GetName(), hosting, present)
		if err != nil {
			return nil, err
		}
		apps = append(apps, uses)
	}
	return apps, nil
}

func presumedUses(frameworks []string) []buildoutput.Uses {
	apps := make([]buildoutput.Uses, 0, len(frameworks))
	for _, framework := range frameworks {
		apps = append(apps, buildoutput.PresumeUses(framework))
	}
	return apps
}

func buildUses(root, app, framework string, hosting buildoutput.Hosting, present bool) (buildoutput.Uses, error) {
	if !present {
		return buildoutput.PresumeUses(framework), nil
	}
	_, err := os.Stat(filepath.Join(buildoutput.AppRoot(root, app), naming.ImageConfigFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return buildoutput.Uses{}, fmt.Errorf("read the image config of %s: %w", app, err)
	}
	return buildoutput.Uses{ISR: hosting.ISR, ImageOptimization: err == nil}, nil
}
