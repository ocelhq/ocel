package commands

import (
	"time"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/pricecache"
	"github.com/ocelhq/ocel/pkg/pricing"
)

type Prices struct {
	Table     pricing.Table
	CachePath string
}

func (i Invocation) OpenPriceCatalog(offline bool) (*pricecache.Catalog, error) {
	path := i.Prices.CachePath
	if path == "" {
		var err error
		if path, err = pricecache.Path(); err != nil {
			return nil, err
		}
	}
	baseURL, token := console.ResolveSession(i.Console.LoadCredentials, time.Now())
	if offline {
		return pricecache.NewOffline(path, baseURL), nil
	}
	return pricecache.New(path, baseURL, token), nil
}
