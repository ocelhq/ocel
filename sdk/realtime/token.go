package realtime

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/types/known/durationpb"

	realtimev1 "ocel.dev/internal/proto/app/realtime/v1"
)

type realtimeOperation string

const (
	realtimeSubscribe realtimeOperation = "subscribe"
	realtimePublish   realtimeOperation = "publish"
)

type mintedToken struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
}

func (d *Realtime) mintTokens(ctx context.Context, subject string, grants []*realtimev1.Grant) (*realtimev1.MintResponse, error) {
	runtime, err := d.connect("Handler")
	if err != nil {
		return nil, err
	}
	minted, err := runtime.Mint(ctx, &realtimev1.MintRequest{Realtime: d.name, Subject: subject, Ttl: durationpb.New(d.settings.ttl), Grants: grants})
	if err != nil {
		return nil, fmt.Errorf("ocel: realtime %q: mint tokens: %w", d.name, err)
	}
	if len(minted.GetTokens()) != len(grants) {
		return nil, fmt.Errorf("ocel: realtime %q: the runtime minted %d tokens for %d grants", d.name, len(minted.GetTokens()), len(grants))
	}
	return minted, nil
}
