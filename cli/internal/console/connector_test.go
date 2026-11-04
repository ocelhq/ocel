package console

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

type connectorService struct {
	consolev1connect.UnimplementedEndpointServiceHandler
	upsert     func(*consolev1.UpsertEndpointRequest) (*consolev1.UpsertEndpointResponse, error)
	list       func() (*consolev1.ListEndpointsResponse, error)
	remove     func(*consolev1.RemoveEndpointRequest) (*consolev1.RemoveEndpointResponse, error)
	setAddress func(*consolev1.SetEndpointAddressRequest) (*consolev1.SetEndpointAddressResponse, error)
	headers    http.Header
	procedure  string
}

func (s *connectorService) Upsert(_ context.Context, req *consolev1.UpsertEndpointRequest) (*consolev1.UpsertEndpointResponse, error) {
	return s.upsert(req)
}

func (s *connectorService) List(context.Context, *consolev1.ListEndpointsRequest) (*consolev1.ListEndpointsResponse, error) {
	return s.list()
}

func (s *connectorService) Remove(_ context.Context, req *consolev1.RemoveEndpointRequest) (*consolev1.RemoveEndpointResponse, error) {
	return s.remove(req)
}

func (s *connectorService) SetAddress(_ context.Context, req *consolev1.SetEndpointAddressRequest) (*consolev1.SetEndpointAddressResponse, error) {
	return s.setAddress(req)
}

func serveConnectors(t *testing.T, service *connectorService) *Client {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := consolev1connect.NewEndpointServiceHandler(service)
	mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service.headers = r.Header.Clone()
		service.procedure = r.URL.Path
		handler.ServeHTTP(w, r)
	})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestUpsertConnectorRegistersTheTargetAsADialConnectorUnderTheSession(t *testing.T) {
	t.Parallel()

	var got *consolev1.UpsertEndpointRequest
	service := &connectorService{upsert: func(req *consolev1.UpsertEndpointRequest) (*consolev1.UpsertEndpointResponse, error) {
		got = req
		return &consolev1.UpsertEndpointResponse{Endpoint: &consolev1.Endpoint{Id: "con_1", Target: req.GetTarget()}}, nil
	}}

	registered, err := serveConnectors(t, service).UpsertConnector(context.Background(), "tok", "staging-box", "vps/sha256:abc/ocel", "vps")
	if err != nil {
		t.Fatalf("UpsertConnector err = %v", err)
	}
	if service.procedure != consolev1connect.EndpointServiceUpsertProcedure {
		t.Errorf("procedure = %s, want %s", service.procedure, consolev1connect.EndpointServiceUpsertProcedure)
	}
	if gotAuth := service.headers.Get("Authorization"); gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok")
	}
	want := &consolev1.UpsertEndpointRequest{Name: "staging-box", Target: "vps/sha256:abc/ocel", Vendor: "vps", Reach: consolev1.EndpointReach_ENDPOINT_REACH_DIAL}
	if !proto.Equal(got, want) {
		t.Errorf("request = %v, want %v", got, want)
	}
	if registered.GetId() != "con_1" {
		t.Errorf("connector id = %q, want con_1", registered.GetId())
	}
}

func TestListConnectorsReturnsEveryConnectorAndFindConnectorPicksOneByTarget(t *testing.T) {
	t.Parallel()

	listed := []*consolev1.Endpoint{
		{Id: "con_1", Target: "vps/sha256:aaaa/ocel"},
		{Id: "con_2", Target: "vps/sha256:bbbb/ocel"},
	}
	service := &connectorService{list: func() (*consolev1.ListEndpointsResponse, error) {
		return &consolev1.ListEndpointsResponse{Endpoints: listed}, nil
	}}
	client := serveConnectors(t, service)

	connectors, err := client.ListConnectors(context.Background(), "tok")
	if err != nil {
		t.Fatalf("ListConnectors err = %v", err)
	}
	if service.procedure != consolev1connect.EndpointServiceListProcedure {
		t.Errorf("procedure = %s, want %s", service.procedure, consolev1connect.EndpointServiceListProcedure)
	}
	if len(connectors) != 2 || connectors[1].GetId() != "con_2" {
		t.Fatalf("connectors = %v, want the two listed", connectors)
	}

	found, err := client.FindConnector(context.Background(), "tok", "vps/sha256:bbbb/ocel")
	if err != nil || found.GetId() != "con_2" {
		t.Errorf("FindConnector = %v, %v, want con_2", found, err)
	}
	missing, err := client.FindConnector(context.Background(), "tok", "vps/sha256:cccc/ocel")
	if err != nil || missing != nil {
		t.Errorf("FindConnector of an unregistered target = %v, %v, want nil", missing, err)
	}
}

func TestSetConnectorAddressSendsOnlyThePartsOfTheAddressTheCallerNamed(t *testing.T) {
	t.Parallel()

	t.Run("sends the id and only the parts of the address the caller named", func(t *testing.T) {
		t.Parallel()

		var got *consolev1.SetEndpointAddressRequest
		service := &connectorService{setAddress: func(req *consolev1.SetEndpointAddressRequest) (*consolev1.SetEndpointAddressResponse, error) {
			got = req
			return &consolev1.SetEndpointAddressResponse{Endpoint: &consolev1.Endpoint{Id: req.GetId()}}, nil
		}}

		_, err := serveConnectors(t, service).SetConnectorAddress(context.Background(), "tok", "con_1", ConnectorAddress{
			URL: "https://connector.example.test",
		})
		if err != nil {
			t.Fatalf("SetConnectorAddress err = %v", err)
		}
		if service.procedure != consolev1connect.EndpointServiceSetAddressProcedure {
			t.Errorf("procedure = %s, want %s", service.procedure, consolev1connect.EndpointServiceSetAddressProcedure)
		}
		want := &consolev1.SetEndpointAddressRequest{Id: "con_1", Url: "https://connector.example.test"}
		if !proto.Equal(got, want) {
			t.Errorf("request = %v, want %v", got, want)
		}
	})

	t.Run("sends the public key and the compute when the caller names them", func(t *testing.T) {
		t.Parallel()

		var got *consolev1.SetEndpointAddressRequest
		service := &connectorService{setAddress: func(req *consolev1.SetEndpointAddressRequest) (*consolev1.SetEndpointAddressResponse, error) {
			got = req
			return &consolev1.SetEndpointAddressResponse{Endpoint: &consolev1.Endpoint{Id: req.GetId()}}, nil
		}}

		_, err := serveConnectors(t, service).SetConnectorAddress(context.Background(), "tok", "con_1", ConnectorAddress{
			URL: "https://connector.example.test", PublicKey: "key", Compute: "container",
		})
		if err != nil {
			t.Fatalf("SetConnectorAddress err = %v", err)
		}
		want := &consolev1.SetEndpointAddressRequest{
			Id: "con_1", Url: "https://connector.example.test", PublicKey: proto.String("key"),
			Compute: consolev1.ComputeKind_COMPUTE_KIND_CONTAINER,
		}
		if !proto.Equal(got, want) {
			t.Errorf("request = %v, want %v", got, want)
		}
	})
}

func TestSetConnectorAddressRefusesAComputeTheConsoleHasNoWordFor(t *testing.T) {
	t.Parallel()

	service := &connectorService{}
	_, err := serveConnectors(t, service).SetConnectorAddress(context.Background(), "tok", "con_1", ConnectorAddress{
		URL: "https://connector.example.test", Compute: "quantum",
	})
	if err == nil {
		t.Fatal("SetConnectorAddress err = nil, want a refusal")
	}
	if service.procedure != "" {
		t.Errorf("the console was called at %s, want no call", service.procedure)
	}
}

func TestComputeNameOfSpellsTheWordsTheProviderUses(t *testing.T) {
	t.Parallel()

	for kind, want := range map[consolev1.ComputeKind]string{
		consolev1.ComputeKind_COMPUTE_KIND_UNSPECIFIED: "",
		consolev1.ComputeKind_COMPUTE_KIND_SERVERLESS:  "serverless",
		consolev1.ComputeKind_COMPUTE_KIND_CONTAINER:   "container",
	} {
		if got := ComputeNameOf(kind); got != want {
			t.Errorf("ComputeNameOf(%v) = %q, want %q", kind, got, want)
		}
	}
}

func TestRemoveConnectorRemovesTheConnectorWithThatID(t *testing.T) {
	t.Parallel()

	var got *consolev1.RemoveEndpointRequest
	service := &connectorService{remove: func(req *consolev1.RemoveEndpointRequest) (*consolev1.RemoveEndpointResponse, error) {
		got = req
		return &consolev1.RemoveEndpointResponse{}, nil
	}}

	if err := serveConnectors(t, service).RemoveConnector(context.Background(), "tok", "con_1"); err != nil {
		t.Fatalf("RemoveConnector err = %v", err)
	}
	if service.procedure != consolev1connect.EndpointServiceRemoveProcedure {
		t.Errorf("procedure = %s, want %s", service.procedure, consolev1connect.EndpointServiceRemoveProcedure)
	}
	if got.GetId() != "con_1" {
		t.Errorf("removed %q, want con_1", got.GetId())
	}
}

func TestAConnectorTheConsoleRefusesToChangeIsPermissionDenied(t *testing.T) {
	t.Parallel()

	service := &connectorService{remove: func(*consolev1.RemoveEndpointRequest) (*consolev1.RemoveEndpointResponse, error) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("Only an owner or an admin may change a connector"))
	}}

	err := serveConnectors(t, service).RemoveConnector(context.Background(), "tok", "con_1")
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("RemoveConnector err = %v, want a permission denied error", err)
	}
}

func TestLivenessOfIsNeverConnectedOnlineOrOfflineFromWhatTheConsoleRecorded(t *testing.T) {
	t.Parallel()

	connected := timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	tests := []struct {
		name      string
		connector *consolev1.Endpoint
		want      Liveness
	}{
		{"never heard from", &consolev1.Endpoint{}, NeverConnected},
		{"heard from and online", &consolev1.Endpoint{ConnectedAt: connected, Online: true}, Online},
		{"heard from and not online", &consolev1.Endpoint{ConnectedAt: connected}, Offline},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := LivenessOf(tt.connector); got != tt.want {
				t.Errorf("LivenessOf = %q, want %q", got, tt.want)
			}
		})
	}
}
