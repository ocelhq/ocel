package server

import (
	"context"
	"reflect"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"google.golang.org/protobuf/types/known/structpb"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func configureRequest(t *testing.T, options map[string]any) *contractv1.ConfigureRequest {
	t.Helper()

	if options == nil {
		return &contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{}}
	}
	fields, err := structpb.NewStruct(options)
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	return &contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{Options: fields}}
}

func TestConfigureDecodesTheOpaqueOptionsIntoTheSession(t *testing.T) {
	t.Parallel()

	s := &Server{}
	req := configureRequest(t, map[string]any{
		"region":       "eu-west-2",
		"transforms":   []any{"./infra/net.transform.ts"},
		"certificates": map[string]any{"app.acme.com": "arn:aws:acm:eu-west-2:1:certificate/x"},
	})
	if _, err := s.Configure(context.Background(), req); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	want := providerConfig{
		Region:       "eu-west-2",
		Transforms:   []string{"./infra/net.transform.ts"},
		Certificates: map[string]string{"app.acme.com": "arn:aws:acm:eu-west-2:1:certificate/x"},
	}
	if got := s.config.get(); !reflect.DeepEqual(got, want) {
		t.Errorf("session config = %+v, want %+v", got, want)
	}
}

func TestConfigureRefusesAnOptionTheProviderDoesNotAccept(t *testing.T) {
	t.Parallel()

	s := &Server{}
	_, err := s.Configure(context.Background(), configureRequest(t, map[string]any{"regionn": "eu-west-2"}))
	if err == nil {
		t.Fatal("Configure err = nil, want an unknown option refused")
	}
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want %v", code, connect.CodeInvalidArgument)
	}
	if !strings.Contains(err.Error(), `"regionn"`) {
		t.Errorf("err = %v, want it to name the offending key", err)
	}
}

func TestConfigureLeavesTheSessionAtTheZeroValueWithoutOptions(t *testing.T) {
	t.Parallel()

	for name, req := range map[string]*contractv1.ConfigureRequest{
		"absent": configureRequest(t, nil),
		"empty":  configureRequest(t, map[string]any{}),
	} {
		t.Run(name, func(t *testing.T) {
			s := &Server{}
			if _, err := s.Configure(context.Background(), req); err != nil {
				t.Fatalf("Configure: %v", err)
			}
			if got := s.config.get(); !reflect.DeepEqual(got, providerConfig{}) {
				t.Errorf("session config = %+v, want the zero value", got)
			}
		})
	}
}
