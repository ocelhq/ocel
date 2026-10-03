package ocel

import (
	"fmt"

	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

type realtimeTransport struct {
	name        string
	answersHost bool
}

func findRealtimeTransport(transport bindingsv1.RealtimeTransport) (realtimeTransport, error) {
	switch transport {
	case bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_APPSYNC_EVENTS:
		return realtimeTransport{name: "appsync-events", answersHost: true}, nil
	case bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_OCEL_GATEWAY:
		return realtimeTransport{name: "ocel-gateway"}, nil
	}
	return realtimeTransport{}, fmt.Errorf("the realtime binding names transport %s, which this SDK does not speak", transport)
}
