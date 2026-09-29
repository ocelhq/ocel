package build

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func HasJS(cfg *projectconfig.Config) (bool, error) {
	if _, err := os.Stat(filepath.Join(cfg.Dir, "package.json")); err == nil {
		return true, nil
	}
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(roots, func(root discovery.Root) bool { return root.Language == language.JS }), nil
}
