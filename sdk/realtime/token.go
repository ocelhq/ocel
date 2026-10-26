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

func (d *Realtime) mintTokens(ctx context.Context, subject string, isConnect bool, subscribeChannels []string) (*realtimev1.MintResponse, error) {
	runtime, err := d.connect("Handler")
	if err != nil {
		return nil, err
	}
	minted, err := runtime.Mint(ctx, &realtimev1.MintRequest{Realtime: d.name, Subject: subject, Ttl: durationpb.New(d.settings.ttl), Connect: isConnect, SubscribeChannels: subscribeChannels})
	if err != nil {
		return nil, fmt.Errorf("ocel: realtime %q: mint tokens: %w", d.name, err)
	}
	switch {
	case len(minted.GetSubscribeTokens()) != len(subscribeChannels):
		return nil, fmt.Errorf("ocel: realtime %q: the runtime minted %d subscribe tokens for %d channels", d.name, len(minted.GetSubscribeTokens()), len(subscribeChannels))
	case isConnect && minted.GetConnectToken() == "":
		return nil, fmt.Errorf("ocel: realtime %q: the runtime minted no connect token", d.name)
	case minted.GetExpiresAt() == nil:
		return nil, fmt.Errorf("ocel: realtime %q: the runtime minted tokens without an expiry", d.name)
	}
	return minted, nil
}
