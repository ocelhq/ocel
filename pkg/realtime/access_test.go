package realtime

import (
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestASubscribeAChannelTakesFindsItsAccessAndNoOtherDoes(t *testing.T) {
	t.Parallel()

	for subscribe, want := range map[resourcesv1.RealtimeSubscribe]ChannelAccess{
		resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_PUBLIC:      ChannelAccessPublic,
		resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE:        ChannelAccessRule,
		resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_UNSPECIFIED: "",
		resourcesv1.RealtimeSubscribe(7):                             "",
	} {
		access, found := FindSubscribeAccess(subscribe)
		if access != want || found != (want != "") {
			t.Errorf("FindSubscribeAccess(%v) = %q, %t, want %q, %t", subscribe, access, found, want, want != "")
		}
	}
}

func TestAPublishAChannelTakesFindsItsAccessAndNoOtherDoes(t *testing.T) {
	t.Parallel()

	for publish, want := range map[resourcesv1.RealtimePublish]ChannelAccess{
		resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER:      ChannelAccessServer,
		resourcesv1.RealtimePublish_REALTIME_PUBLISH_RULE:        ChannelAccessRule,
		resourcesv1.RealtimePublish_REALTIME_PUBLISH_UNSPECIFIED: "",
		resourcesv1.RealtimePublish(7):                           "",
	} {
		access, found := FindPublishAccess(publish)
		if access != want || found != (want != "") {
			t.Errorf("FindPublishAccess(%v) = %q, %t, want %q, %t", publish, access, found, want, want != "")
		}
	}
}
