package providerserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func locateRouteTable(coordinate naming.Coordinate, table router.RouteTable) (router.RouteTableLocation, []byte, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, table.Table); err != nil {
		return router.RouteTableLocation{}, nil, refusal.Refuse(refusal.CodeInvalid, "the route table app %s routes by is not JSON (%v); rebuild the app", coordinate.App, err)
	}
	digest := sha256.Sum256(compact.Bytes())
	return router.RouteTableLocation{Format: table.Format, Key: coordinate.RouteTableKey(hex.EncodeToString(digest[:]))}, compact.Bytes(), nil
}

func (s *sharedStack) storeRouteTable(ctx context.Context, kind router.Kind, app, key string, table []byte) error {
	paired, err := s.findRouter(kind)
	if err != nil {
		return err
	}
	tables := paired.Hooks().RouteTables
	if tables == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"app %s is routed by its route table at %s, and the %s router it pairs with keeps no route table for that edge to read",
			app, describeFront(s.front.Kind()), kind)
	}
	if err := tables.Store(ctx, paired.read(), key, table); err != nil {
		return fmt.Errorf("store the route table app %s routes by: %w", app, err)
	}
	return nil
}

func (s *sharedStack) forgetRouteTable(ctx context.Context, key string) error {
	var errs []error
	for _, kind := range s.listRouterKinds() {
		paired, err := s.findRouter(kind)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if tables := paired.Hooks().RouteTables; tables != nil {
			if err := tables.Forget(ctx, paired.read(), key); err != nil {
				errs = append(errs, fmt.Errorf("forget the route table %s: %w", key, err))
			}
		}
	}
	return errors.Join(errs...)
}
