package realtime

import (
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type ChannelAccess string

const (
	ChannelAccessPublic ChannelAccess = "public"
	ChannelAccessRule   ChannelAccess = "rule"
	ChannelAccessServer ChannelAccess = "server"
)

var subscribeAccesses = map[resourcesv1.RealtimeSubscribe]ChannelAccess{
	resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_PUBLIC: ChannelAccessPublic,
	resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE:   ChannelAccessRule,
}

var publishAccesses = map[resourcesv1.RealtimePublish]ChannelAccess{
	resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER: ChannelAccessServer,
	resourcesv1.RealtimePublish_REALTIME_PUBLISH_RULE:   ChannelAccessRule,
}

func FindSubscribeAccess(subscribe resourcesv1.RealtimeSubscribe) (ChannelAccess, bool) {
	access, found := subscribeAccesses[subscribe]
	return access, found
}

func FindPublishAccess(publish resourcesv1.RealtimePublish) (ChannelAccess, bool) {
	access, found := publishAccesses[publish]
	return access, found
}
