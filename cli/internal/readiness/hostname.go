package readiness

import (
	"fmt"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/prerequisite"
)

type NoHostnameError struct {
	Slug       string
	ConfigPath string
	Vendor     string
}

func (e NoHostnameError) Error() string {
	return fmt.Sprintf("%s declares no production hostname, and this deploy has nowhere to serve without one.\nAdd one under \"domains\" in %s and try again",
		e.Slug, e.configName())
}

func (NoHostnameError) Missing() prerequisite.Kind { return prerequisite.Domain }

func (e NoHostnameError) Finding() string {
	vendor := e.Vendor
	if vendor == "" {
		vendor = "This provider"
	}
	return fmt.Sprintf("%s serves apps on a hostname you own, and %s declares none.", vendor, e.Slug)
}

func (e NoHostnameError) Remedy() string {
	return fmt.Sprintf("add a production hostname under \"domains\" in %s, then run `ocel deploy`", e.configName())
}

func (e NoHostnameError) configName() string { return filepath.Base(e.ConfigPath) }
