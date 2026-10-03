package proxy

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/naming"
	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/realtime/v1/realtimev1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

type Records interface {
	Value(key string) string
}

type Transport func(ctx context.Context, properties *bindingsv1.RealtimeProperties, req *realtimev1.PublishRequest) error

type Service struct {
	records   Records
	transport Transport
}

var _ realtimev1connect.RealtimeServiceHandler = (*Service)(nil)

func NewService(records Records, transport Transport) *Service {
	return &Service{records: records, transport: transport}
}

func (s *Service) Publish(ctx context.Context, req *realtimev1.PublishRequest) (*realtimev1.PublishResponse, error) {
	properties, err := s.readProperties(req.GetRealtime())
	if err != nil {
		return nil, err
	}
	if err := s.transport(ctx, properties, req); err != nil {
		return nil, err
	}
	return &realtimev1.PublishResponse{}, nil
}

func (s *Service) readProperties(name string) (*bindingsv1.RealtimeProperties, error) {
	key := naming.ResourceEnvName(bindingsv1.BindingType_BINDING_TYPE_REALTIME, name)
	raw := s.records.Value(key)
	if raw == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this runtime holds no realtime binding for %q, so it has nowhere to publish", name))
	}
	record := &bindingsv1.Binding{}
	if err := protojson.Unmarshal([]byte(raw), record); err != nil || record.GetRealtime() == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the binding delivered under %s is not a realtime binding record", key))
	}
	return record.GetRealtime(), nil
}

func RefuseOtherTransport(properties *bindingsv1.RealtimeProperties, served bindingsv1.RealtimeTransport) error {
	if properties.GetTransport() == served {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the realtime binding names transport %s, and this runtime publishes only over %s", properties.GetTransport(), served))
}
