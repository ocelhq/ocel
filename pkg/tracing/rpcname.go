package tracing

import "go.opentelemetry.io/otel/attribute"

func NameRPCSpan(system, service, method string) (string, []attribute.KeyValue) {
	return service + "/" + method, []attribute.KeyValue{
		attribute.String("rpc.system", system),
		attribute.String("rpc.service", service),
		attribute.String("rpc.method", method),
	}
}
