package ports

import (
	"context"
	"slices"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	resourceNamesAttribute = "gcp.resource.destination.id"
	resourceNamePrefix     = "projects/"
	resourceNamesLimit     = 64
)

type rpcSpans struct {
	stats.Handler
}

func newRPCSpanOptions() []option.ClientOption {
	return []option.ClientOption{
		option.WithTelemetryDisabled(),
		option.WithGRPCDialOption(grpc.WithStatsHandler(rpcSpans{otelgrpc.NewClientHandler()})),
	}
}

func (h rpcSpans) HandleRPC(ctx context.Context, rpc stats.RPCStats) {
	if sent, ok := rpc.(*stats.OutPayload); ok && sent.Client {
		if message, ok := sent.Payload.(proto.Message); ok {
			if names := readResourceNames(message.ProtoReflect(), nil); len(names) > 0 {
				trace.SpanFromContext(ctx).SetAttributes(attribute.StringSlice(resourceNamesAttribute, names))
			}
		}
	}
	h.Handler.HandleRPC(ctx, rpc)
}

func readResourceNames(message protoreflect.Message, names []string) []string {
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case len(names) >= resourceNamesLimit, field.IsMap():
		case field.IsList():
			list := value.List()
			for i := range list.Len() {
				names = readResourceName(field, list.Get(i), names)
			}
		default:
			names = readResourceName(field, value, names)
		}
		return len(names) < resourceNamesLimit
	})
	return names
}

func readResourceName(field protoreflect.FieldDescriptor, value protoreflect.Value, names []string) []string {
	switch field.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return readResourceNames(value.Message(), names)
	case protoreflect.StringKind:
		if name := value.String(); strings.HasPrefix(name, resourceNamePrefix) && !slices.Contains(names, name) && len(names) < resourceNamesLimit {
			return append(names, name)
		}
	}
	return names
}
