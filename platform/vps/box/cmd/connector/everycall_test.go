package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/endpoint"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/providerhost"
	"github.com/ocelhq/ocel/pkg/scope"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type boxThatRefuses struct{}

func (boxThatRefuses) Destination() ssh.Destination {
	return ssh.Destination{Written: "box.example.com", Port: 22, User: "ocel-deploy"}
}

func (boxThatRefuses) Stream(context.Context, string, io.Reader) (ssh.Result, error) {
	return ssh.Result{Stderr: "this is no box", Code: 1}, nil
}

func (b boxThatRefuses) Run(ctx context.Context, command string) (string, error) {
	return "", fmt.Errorf("%s: this is no box", command)
}

func (boxThatRefuses) RunLines(_ context.Context, command string, _ func(ssh.Line) error) error {
	return fmt.Errorf("%s: this is no box", command)
}

func (boxThatRefuses) Preflight(context.Context) (ssh.Facts, error) {
	return ssh.Facts{}, fmt.Errorf("this is no box")
}

func (boxThatRefuses) ForwardPort(context.Context, string) (string, func(), error) {
	return "", nil, fmt.Errorf("this is no box")
}

func (boxThatRefuses) ForwardSpans() string { return "" }

type panics struct {
	mu   sync.Mutex
	seen []string
}

func (p *panics) catching(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				p.mu.Lock()
				p.seen = append(p.seen, fmt.Sprintf("%s: %v\n%s", r.URL.Path, recovered, debug.Stack()))
				p.mu.Unlock()
				http.Error(w, "panicked", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

var onTheTestBox = provider.Options{"ssh": map[string]any{"host": "box.example.com", "user": "ocel-deploy"}}

func everyUserScope() []scopev1.Scope { return scope.ListUserScopes() }

func endpointServing(t *testing.T, caught *panics) *httptest.Server {
	t.Helper()
	control, err := providerhost.New(controlHost(fixedBox("box.example.com", boxThatRefuses{})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.Close)
	allowed := make([]string, 0, len(everyUserScope()))
	for _, held := range everyUserScope() {
		allowed = append(allowed, scope.ClaimOf(held))
	}
	handler, err := endpoint.New(endpoint.Spec{
		Config: endpoint.Config{Console: "https://console.example.com", EndpointID: "endpoint-1", OrganizationID: "org-1", AllowedScopes: allowed},
		Verify: func(context.Context, string) (endpoint.Caller, error) {
			return endpoint.NewUserCaller("https://console.example.com", "org-1", "user-1", scopev1.Role_ROLE_OWNER, everyUserScope())
		},
		Mounts: append(control.Mounts(), variablesMount(t.TempDir())),
	})
	if err != nil {
		t.Fatal(err)
	}
	served := httptest.NewServer(caught.catching(handler))
	t.Cleanup(served.Close)
	return served
}

func sessionServing(t *testing.T, caught *panics) *httptest.Server {
	t.Helper()
	served := httptest.NewServer(caught.catching(providerserver.ConformanceMux(providerserver.Config{
		Version: "test",
		New: func(ctx context.Context, settings provider.Settings) (provider.Provider, error) {
			return vps.NewControl(ctx, settings, boxThatRefuses{})
		},
	})))
	t.Cleanup(served.Close)
	return served
}

func mustStruct(t *testing.T, options provider.Options) *structpb.Struct {
	t.Helper()
	encoded, err := structpb.NewStruct(options)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func filled(message protoreflect.Message, depth, variant int) {
	fields := message.Descriptor().Fields()
	for i := range fields.Len() {
		field := fields.Get(i)
		if field.IsList() || field.IsMap() || field.ContainingOneof() != nil {
			continue
		}
		name := string(field.Name())
		switch field.Kind() {
		case protoreflect.StringKind:
			message.Set(field, protoreflect.ValueOfString(sample(name)))
		case protoreflect.EnumKind:
			if values := field.Enum().Values(); values.Len() > 1 {
				message.Set(field, protoreflect.ValueOfEnum(values.Get(min(variant, values.Len()-1)).Number()))
			}
		case protoreflect.MessageKind:
			if depth > 0 && !strings.HasPrefix(string(field.Message().FullName()), "google.") {
				filled(message.Mutable(field).Message(), depth-1, variant)
			}
		}
	}
}

func sample(field string) string {
	switch {
	case strings.Contains(field, "slug"), strings.Contains(field, "project"), field == "name":
		return "shop"
	case strings.Contains(field, "host"), strings.Contains(field, "domain"):
		return "shop.example.com"
	case strings.Contains(field, "dir"):
		return "/tmp"
	}
	return "x"
}

const enumVariants = 4

var served = []protoreflect.ServiceDescriptor{
	contractv1.File_provider_contract_v1_contract_proto.Services().ByName("ProviderService"),
	costv1.File_provider_cost_v1_cost_proto.Services().ByName("CostService"),
	variablestorev1.File_provider_variablestore_v1_variablestore_proto.Services().ByName("VariableStoreService"),
}

func call(ctx context.Context, server *httptest.Server, method protoreflect.MethodDescriptor, header http.Header, variant int) error {
	initializer := func(spec connect.Spec, msg any) error {
		if dynamic, ok := msg.(*dynamicpb.Message); ok {
			*dynamic = *dynamicpb.NewMessage(spec.Schema.(protoreflect.MethodDescriptor).Output())
		}
		return nil
	}
	procedure := "/" + string(method.Parent().FullName()) + "/" + string(method.Name())
	client := connect.NewClient[dynamicpb.Message, dynamicpb.Message](server.Client(), server.URL+procedure,
		connect.WithSchema(method), connect.WithResponseInitializer(initializer))
	request := dynamicpb.NewMessage(method.Input())
	filled(request, 2, variant)
	if method.IsStreamingServer() {
		asked := connect.NewRequest(request)
		for key, values := range header {
			asked.Header()[key] = values
		}
		stream, err := client.CallServerStream(ctx, asked)
		if err != nil {
			return err
		}
		defer stream.Close()
		for stream.Receive() {
		}
		return stream.Err()
	}
	asked := connect.NewRequest(request)
	for key, values := range header {
		asked.Header()[key] = values
	}
	_, err := client.CallUnary(ctx, asked)
	return err
}

func TestEveryCallTheBoxServesAnswersOrRefusesWithoutPanicking(t *testing.T) {
	caught := &panics{}
	ctx := context.Background()

	onEndpoint := endpointServing(t, caught)
	sessionless := http.Header{providerhost.HeaderSlug: {"shop"}, providerhost.HeaderTier: {"production"}}

	inSession := sessionServing(t, caught)
	session := contractv1connect.ProviderServiceName
	client := connect.NewClient[contractv1.ConfigureRequest, contractv1.ConfigureResponse](inSession.Client(), inSession.URL+"/"+session+"/Configure")
	if _, err := client.CallUnary(ctx, connect.NewRequest(&contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{
		ProjectDir: t.TempDir(), Slug: "shop", Options: mustStruct(t, onTheTestBox),
	}})); err != nil {
		t.Fatalf("Configure() = %v", err)
	}

	for _, service := range served {
		methods := service.Methods()
		for i := range methods.Len() {
			method := methods.Get(i)
			t.Run(string(service.Name())+"/"+string(method.Name()), func(t *testing.T) {
				for variant := 1; variant <= enumVariants; variant++ {
					_ = call(ctx, onEndpoint, method, sessionless, variant)
					_ = call(ctx, inSession, method, nil, variant)
				}
			})
		}
	}
	for _, seen := range caught.seen {
		t.Errorf("a call panicked: %s", seen)
	}
}
