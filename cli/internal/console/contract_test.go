package console

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/consolecontract"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
)

var contractBinding = consolecontract.Binding{Values: map[string]string{
	"session.token":      "Zt7dOq1bS3x5cW8yK2mN4pR6vT9uX0aB",
	"organization.id":    "Hq2Lw8Nc5Vz1Rk7Tm4Yp9Bd3",
	"organization.slug":  "test-org",
	"device.code":        "Gx3uYw9VbKqL2mPz7RtN4sHd",
	"user.email":         "test@example.test",
	"cloud.api_url":      "https://cloud.example.test",
	"cloud.provider_url": "https://providers.cloud.example.test",
	"cloud.audience":     "https://cloud.example.test",
}}

type contractCall struct {
	send    func(ctx context.Context, c *Client, values map[string]string) (any, error)
	refused func(error) bool
}

var contractCalls = map[string]contractCall{
	"device-code": {send: func(ctx context.Context, c *Client, _ map[string]string) (any, error) {
		decoded, err := c.RequestDeviceCode(ctx)
		return decoded, err
	}},
	"device-token-pending": {
		send: func(ctx context.Context, c *Client, values map[string]string) (any, error) {
			decoded, err := c.PollToken(ctx, values["device.code"])
			return decoded, err
		},
		refused: IsAuthorizationPending,
	},
	"device-token": {send: func(ctx context.Context, c *Client, values map[string]string) (any, error) {
		decoded, err := c.PollToken(ctx, values["device.code"])
		return decoded, err
	}},
	"session": {send: func(ctx context.Context, c *Client, values map[string]string) (any, error) {
		decoded, err := c.GetSession(ctx, values["session.token"])
		return decoded, err
	}},
	"organization-list": {send: func(ctx context.Context, c *Client, values map[string]string) (any, error) {
		decoded, err := c.ListOrganizations(ctx, values["session.token"])
		return decoded, err
	}},
	"organization-set-active": {send: func(ctx context.Context, c *Client, values map[string]string) (any, error) {
		return nil, c.SetActiveOrganization(ctx, values["session.token"], values["organization.id"])
	}},
	"sign-out": {send: func(ctx context.Context, c *Client, values map[string]string) (any, error) {
		return nil, c.SignOut(ctx, values["session.token"])
	}},
	"console-describe": {send: func(ctx context.Context, c *Client, _ map[string]string) (any, error) {
		return asProtoJSON(c.Describe(ctx))
	}},
	"cloud-token": {send: func(ctx context.Context, c *Client, values map[string]string) (any, error) {
		decoded, err := c.IssueCloudToken(ctx, values["session.token"], values["organization.id"], []cloudv1.Scope{cloudv1.Scope_SCOPE_APPS_READ})
		return decoded, err
	}},
	"cloud-token-without-session": {
		send: func(ctx context.Context, c *Client, values map[string]string) (any, error) {
			_, err := c.IssueCloudToken(ctx, "", values["organization.id"], []cloudv1.Scope{cloudv1.Scope_SCOPE_APPS_READ})
			return nil, err
		},
		refused: func(err error) bool { return connect.CodeOf(err) == connect.CodeUnauthenticated },
	},
}

func asProtoJSON[T proto.Message](msg T, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	encoded, err := protojson.Marshal(msg)
	return json.RawMessage(encoded), err
}

func TestEveryRequestTheCLISendsMatchesAConsoleFixture(t *testing.T) {
	fixtures := consolecontract.ReadFixtures(t, "testdata/contract")
	var names []string
	for _, fixture := range fixtures {
		names = append(names, fixture.Name)
		if _, called := contractCalls[fixture.Name]; !called {
			t.Errorf("fixture %s has no client call", fixture.Name)
		}
	}
	for name := range contractCalls {
		if !slices.Contains(names, name) {
			t.Errorf("client call %s has no fixture", name)
		}
	}

	for _, fixture := range fixtures {
		call, called := contractCalls[fixture.Name]
		if !called {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			server := consolecontract.ServeFixture(t, fixture, contractBinding)
			decoded, err := call.send(context.Background(), New(server.URL), contractBinding.Values)

			if fixture.Response.Status >= 300 {
				requireRefusal(t, fixture, contractBinding, err, call.refused)
				return
			}
			if err != nil {
				t.Fatalf("the client refused a %d answer: %v", fixture.Response.Status, err)
			}
			if decoded != nil {
				consolecontract.RequireDecodedBody(t, fixture, contractBinding, decoded)
			}
		})
	}
}

func requireRefusal(t *testing.T, fixture consolecontract.Fixture, binding consolecontract.Binding, err error, refused func(error) bool) {
	t.Helper()
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		requireConnectRefusal(t, fixture, binding, connectErr, refused)
		return
	}
	var consoleErr *Error
	if !errors.As(err, &consoleErr) {
		t.Fatalf("err = %v, want a *console.Error", err)
	}
	if consoleErr.StatusCode != fixture.Response.Status {
		t.Errorf("status = %d, want %d", consoleErr.StatusCode, fixture.Response.Status)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(fixture.Resolve(t, binding).Response.Body, &body); err != nil {
		t.Fatal(err)
	}
	if consoleErr.Code != body.Error {
		t.Errorf("code = %q, want %q", consoleErr.Code, body.Error)
	}
	if refused == nil {
		t.Fatalf("fixture %s answers %d, but its call names no predicate that recognises it", fixture.Name, fixture.Response.Status)
	}
	if !refused(err) {
		t.Errorf("the call's predicate does not recognise %v", err)
	}
}

func requireConnectRefusal(t *testing.T, fixture consolecontract.Fixture, binding consolecontract.Binding, err *connect.Error, refused func(error) bool) {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(fixture.Resolve(t, binding).Response.Body, &body); err != nil {
		t.Fatal(err)
	}
	if err.Code().String() != body.Code {
		t.Errorf("code = %q, want %q", err.Code(), body.Code)
	}
	if refused == nil {
		t.Fatalf("fixture %s answers %d, but its call names no predicate that recognises it", fixture.Name, fixture.Response.Status)
	}
	if !refused(err) {
		t.Errorf("the call's predicate does not recognise %v", err)
	}
}
