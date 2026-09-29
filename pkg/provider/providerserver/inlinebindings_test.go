package providerserver_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

const inlinePassword = "s3cret-pw"

type postgresServer struct {
	addr     string
	startups atomic.Int32
}

func servePostgres(t *testing.T, serverVersionNum string, busyFor int32) *postgresServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	server := &postgresServer{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go server.answer(conn, serverVersionNum, busyFor)
		}
	}()
	return server
}

func (s *postgresServer) answer(conn net.Conn, serverVersionNum string, busyFor int32) {
	defer conn.Close()
	backend := pgproto3.NewBackend(conn, conn)
	for {
		msg, err := backend.ReceiveStartupMessage()
		if err != nil {
			return
		}
		if _, startup := msg.(*pgproto3.StartupMessage); startup {
			break
		}
		if _, err := conn.Write([]byte("N")); err != nil {
			return
		}
	}
	if s.startups.Add(1) <= busyFor {
		backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: "53300", Message: "sorry, too many clients already"})
		_ = backend.Flush()
		return
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if backend.Flush() != nil {
		return
	}
	if _, err := backend.Receive(); err != nil {
		return
	}
	backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{
		{Name: []byte("?column?"), DataTypeOID: 23, DataTypeSize: 4},
		{Name: []byte("current_setting"), DataTypeOID: 25, DataTypeSize: -1},
	}})
	backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("1"), []byte(serverVersionNum)}})
	backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	_ = backend.Flush()
	_, _ = backend.Receive()
}

func (s *postgresServer) url() string {
	return "postgres://app:" + inlinePassword + "@" + s.addr + "/orders?sslmode=disable"
}

func closedAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

var inlineOrders = naming.InlineRecordName(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "orders")

func inlinePostgresRequest(url, declaredVersion string) *contractv1.DeployRequest {
	req := externalBindingRequest("orders", inlineOrders, bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
	req.Manifest.Resources[0].Config = &contractv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{Version: declaredVersion}}
	req.InlineBindings = []*bindingsv1.Binding{{
		Name:       inlineOrders,
		Source:     "ocel.json",
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Url: url}},
	}}
	return req
}

var inlineUploads = naming.InlineRecordName(resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, "uploads")

func inlineBucketRequest(bucket *resourcesv1.BucketConfig, props *bindingsv1.BucketProperties) *contractv1.DeployRequest {
	req := externalBindingRequest("uploads", inlineUploads, bindingsv1.BindingType_BINDING_TYPE_BUCKET)
	req.Manifest.Usages[0].Resource = "uploads"
	req.Manifest.Resources[0].Config = &contractv1.ManifestResource_Bucket{Bucket: bucket}
	req.InlineBindings = []*bindingsv1.Binding{{
		Name:       inlineUploads,
		Source:     "ocel.json",
		Properties: &bindingsv1.Binding_Bucket{Bucket: props},
	}}
	return req
}

func storedBindings(t *testing.T, vendor *fake.Provider) map[string]envvars.StoredBinding {
	t.Helper()
	store := envvars.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	listed, err := store.ListBindings(context.Background(), envvars.Scope{Project: "shop", Tier: environment.TierProduction}, "")
	if err != nil {
		t.Fatalf("ListBindings: %v", err)
	}
	stored := map[string]envvars.StoredBinding{}
	for _, binding := range listed {
		stored[binding.Name] = binding
	}
	return stored
}

func inlineRefusal(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.DeployRequest) string {
	t.Helper()
	result, _, err := deployStream(t, client, req)
	if err != nil {
		return err.Error()
	}
	if result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want the inline binding refused")
	}
	return result.GetError()
}

func said(events []*progressv1.OperationEvent, level progressv1.Level) []string {
	var messages []string
	for _, event := range events {
		if event.GetBody() == nil && event.GetLevel() == level {
			messages = append(messages, event.GetMessage())
		}
	}
	return messages
}

func TestDeployPublishesTheInlineBindingsItCarries(t *testing.T) {
	t.Run("the record is checked against the server and published under the config's owner", func(t *testing.T) {
		builtProject(t)
		client, vendor := deployServed(t)
		server := servePostgres(t, "170004", 0)

		result, _ := deploy(t, client, inlinePostgresRequest(server.url(), "17"))
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want the inline record published and bound", result.GetError())
		}
		stored, published := storedBindings(t, vendor)[inlineOrders]
		if !published || stored.Owner != naming.InlineRecordOwner {
			t.Fatalf("records = %v, want %s published by %s", storedBindings(t, vendor), inlineOrders, naming.InlineRecordOwner)
		}
		store := envvars.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
		resolved, err := store.ResolveBinding(context.Background(), envvars.Scope{Project: "shop", Tier: environment.TierProduction}, "", inlineOrders)
		if err != nil || !strings.Contains(string(resolved.Value), inlinePassword) {
			t.Errorf("ResolveBinding = %v, want the published record to keep the url the app connects with", err)
		}
		if strings.Contains(string(stored.Record), inlinePassword) {
			t.Error("the record's readable summary repeats the password")
		}
	})

	t.Run("a record no inline binding carries any more is removed once the deploy is promoted", func(t *testing.T) {
		builtProject(t)
		client, vendor := deployServed(t)
		server := servePostgres(t, "170004", 0)
		dropped := naming.InlineRecordName(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "archive")
		publishRecord(t, vendor, environment.TierProduction, naming.InlineRecordOwner, postgresRecord(dropped, "ocel.json"))
		publishRecord(t, vendor, environment.TierProduction, "terraform", postgresRecord("ledger", "terraform"))

		result, _ := deploy(t, client, inlinePostgresRequest(server.url(), "17"))
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q", result.GetError())
		}
		stored := storedBindings(t, vendor)
		if _, kept := stored[dropped]; kept {
			t.Errorf("records = %v, want %s removed: no inline binding keeps it", stored, dropped)
		}
		if _, kept := stored["ledger"]; !kept {
			t.Errorf("records = %v, want the record another publisher owns left alone", stored)
		}
		if _, kept := stored[inlineOrders]; !kept {
			t.Errorf("records = %v, want the carried record kept", stored)
		}
	})

	t.Run("a dry run checks the record and publishes nothing", func(t *testing.T) {
		builtProject(t)
		client, vendor := deployServed(t)
		server := servePostgres(t, "170004", 0)
		req := inlinePostgresRequest(server.url(), "17")
		req.Dry = true

		result, _ := deploy(t, client, req)
		if !result.GetSuccess() {
			t.Fatalf("Deploy(dry) = %q, want the plan made with the record the deploy would publish", result.GetError())
		}
		if server.startups.Load() == 0 {
			t.Error("the dry run never reached the server, want the record checked before the plan")
		}
		if stored := storedBindings(t, vendor); len(stored) != 0 {
			t.Errorf("records = %v, want a dry run to publish nothing", stored)
		}
	})

	t.Run("a server too busy to answer at first is asked again", func(t *testing.T) {
		builtProject(t)
		client, _ := deployServed(t)
		server := servePostgres(t, "170004", 1)

		result, _ := deploy(t, client, inlinePostgresRequest(server.url(), "17"))
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want a server that refused one connection for load asked again", result.GetError())
		}
	})
}

func TestDeployRefusesAnInlineBindingItCannotVerify(t *testing.T) {
	t.Run("an unreachable server is refused naming the binding and never the password", func(t *testing.T) {
		builtProject(t)
		client, vendor := deployServed(t)
		url := "postgres://app:" + inlinePassword + "@" + closedAddress(t) + "/orders?sslmode=disable"

		message := inlineRefusal(t, client, inlinePostgresRequest(url, "17"))
		if !strings.Contains(message, "bindings.postgres.orders") {
			t.Errorf("refusal = %q, want the binding named as the config spells it", message)
		}
		if strings.Contains(message, inlinePassword) {
			t.Errorf("refusal = %q, repeats the password", message)
		}
		if stored := storedBindings(t, vendor); len(stored) != 0 {
			t.Errorf("records = %v, want nothing published for a refused binding", stored)
		}
	})

	t.Run("a server of another major version is refused naming both", func(t *testing.T) {
		builtProject(t)
		client, _ := deployServed(t)
		server := servePostgres(t, "150008", 0)

		message := inlineRefusal(t, client, inlinePostgresRequest(server.url(), "17"))
		for _, want := range []string{"declares postgres 17", "serves postgres 15", "bindings.postgres.orders"} {
			if !strings.Contains(message, want) {
				t.Errorf("refusal = %q, want it to contain %q", message, want)
			}
		}
	})

	t.Run("a server numbered before postgres 10 is read as the two-part major it is", func(t *testing.T) {
		builtProject(t)
		client, _ := deployServed(t)
		server := servePostgres(t, "90624", 0)

		if result, _ := deploy(t, client, inlinePostgresRequest(server.url(), "9.6")); !result.GetSuccess() {
			t.Errorf("Deploy(9.6 against 9.6.24) = %q", result.GetError())
		}
		if message := inlineRefusal(t, client, inlinePostgresRequest(server.url(), "9.5")); !strings.Contains(message, "serves postgres 9.6") {
			t.Errorf("Deploy(9.5 against 9.6.24) = %q, want it refused naming 9.6", message)
		}
	})

	t.Run("a record no resource in the manifest binds is refused", func(t *testing.T) {
		req := inlinePostgresRequest("postgres://unused", "17")
		req.InlineBindings[0].Name = naming.InlineRecordName(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "stray")
		message := refusedDeploy(t, req, nil)
		if !strings.Contains(message, "ocel:postgres.stray") {
			t.Errorf("refusal = %q, want the stray record named", message)
		}
	})

	t.Run("a record named outside the inline prefix is refused", func(t *testing.T) {
		req := inlinePostgresRequest("postgres://unused", "17")
		req.InlineBindings[0].Name = "orders"
		req.Manifest.Resources[0].Binding = "orders"
		message := refusedDeploy(t, req, nil)
		if !strings.Contains(message, naming.InlineRecordPrefix) {
			t.Errorf("refusal = %q, want the inline prefix named", message)
		}
	})
}

func TestDeployChecksAnInlineBucketThroughTheProvider(t *testing.T) {
	props := func() *bindingsv1.BucketProperties {
		return &bindingsv1.BucketProperties{
			Bucket: "acme", Endpoint: "https://s3.example.com", PublicBaseUrl: "https://cdn.acme.com",
			AccessKeyId: "AKID", SecretAccessKey: inlinePassword,
		}
	}
	served := func(t *testing.T, check func(context.Context, *bindingsv1.BucketProperties, bool, []string) ([]string, error)) (contractv1connect.ProviderServiceClient, *fake.Provider) {
		t.Helper()
		builtProject(t)
		client, vendor := deployServed(t)
		vendor.WithHooks(func(h *provider.Hooks) { h.CheckBucket = check })
		return client, vendor
	}

	t.Run("hands the check what the code declares and passes its warnings on", func(t *testing.T) {
		var asked []string
		client, vendor := served(t, func(_ context.Context, _ *bindingsv1.BucketProperties, public bool, origins []string) ([]string, error) {
			if public {
				asked = append(asked, "public")
			}
			asked = append(asked, origins...)
			return []string{"could not read the policy"}, nil
		})

		result, events := deploy(t, client, inlineBucketRequest(&resourcesv1.BucketConfig{Public: true, AllowedOrigins: []string{"https://acme.com"}}, props()))
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q", result.GetError())
		}
		if !slices.Equal(asked, []string{"public", "https://acme.com"}) {
			t.Errorf("the check was asked for %v", asked)
		}
		warnings := said(events, progressv1.Level_LEVEL_WARN)
		if !slices.ContainsFunc(warnings, func(w string) bool {
			return strings.Contains(w, "bindings.bucket.uploads") && strings.Contains(w, "could not read the policy")
		}) {
			t.Errorf("warnings = %q, want the check's warning, naming the binding", warnings)
		}
		if _, published := storedBindings(t, vendor)[inlineUploads]; !published {
			t.Error("the bucket's record was not published")
		}
	})

	t.Run("a public bucket with no public address is refused before the store is asked", func(t *testing.T) {
		client, _ := served(t, func(context.Context, *bindingsv1.BucketProperties, bool, []string) ([]string, error) {
			t.Error("the store was asked about a binding the config already rules out")
			return nil, nil
		})
		unaddressed := props()
		unaddressed.PublicBaseUrl = ""

		if message := inlineRefusal(t, client, inlineBucketRequest(&resourcesv1.BucketConfig{Public: true}, unaddressed)); !strings.Contains(message, "publicBaseUrl") {
			t.Errorf("refusal = %q, want the missing publicBaseUrl named", message)
		}
	})

	t.Run("a refusal from the store names the binding and never the secret key", func(t *testing.T) {
		client, vendor := served(t, func(_ context.Context, record *bindingsv1.BucketProperties, _ bool, _ []string) ([]string, error) {
			return nil, fmt.Errorf("bucket acme did not answer: signed with %s: %w", record.GetSecretAccessKey(), errors.New("NoSuchBucket"))
		})

		message := inlineRefusal(t, client, inlineBucketRequest(&resourcesv1.BucketConfig{}, props()))
		if !strings.Contains(message, "bindings.bucket.uploads") || !strings.Contains(message, "NoSuchBucket") {
			t.Errorf("refusal = %q, want the binding and the cause named", message)
		}
		if strings.Contains(message, inlinePassword) {
			t.Errorf("refusal = %q, repeats the secret key", message)
		}
		if stored := storedBindings(t, vendor); len(stored) != 0 {
			t.Errorf("records = %v, want nothing published", stored)
		}
	})
}

func inlineOrdersAt(url string) *bindingsv1.Binding {
	return &bindingsv1.Binding{
		Name:       inlineOrders,
		Source:     "ocel.json",
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Url: url}},
	}
}

func resolvedOrders(t *testing.T, vendor *fake.Provider) envvars.StoredBinding {
	t.Helper()
	store := envvars.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	resolved, err := store.ResolveBinding(context.Background(), envvars.Scope{Project: "shop", Tier: environment.TierProduction}, "", inlineOrders)
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}
	return resolved
}

func TestAFailedDeployLeavesTheInlineRecordsTheLiveReleaseReads(t *testing.T) {
	const live = "postgres://app:" + inlinePassword + "@live.example:5432/orders"

	t.Run("a deploy whose provisioning fails never wrote the record", func(t *testing.T) {
		builtProject(t)
		client, vendor := deployServed(t)
		server := servePostgres(t, "170004", 0)
		publishRecord(t, vendor, environment.TierProduction, naming.InlineRecordOwner, inlineOrdersAt(live))
		vendor.FakeStacks().Entering(func(spec provider.StackSpec) error {
			if spec.App == nil {
				return nil
			}
			return errors.New("the function could not be created")
		})

		if result, _ := deploy(t, client, inlinePostgresRequest(server.url(), "17")); result.GetSuccess() {
			t.Fatal("Deploy() succeeded, want the provisioning failure")
		}
		if resolved := resolvedOrders(t, vendor); !strings.Contains(string(resolved.Value), "live.example") || resolved.Version != 1 {
			t.Errorf("record = %q v%d, want the live release's record untouched", resolved.Value, resolved.Version)
		}
	})

	t.Run("a deploy overtaken at promotion puts the record back", func(t *testing.T) {
		builtProject(t)
		client, vendor := deployServed(t)
		server := servePostgres(t, "170004", 0)
		publishRecord(t, vendor, environment.TierProduction, naming.InlineRecordOwner, inlineOrdersAt(live))
		releases := seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1")
		overtakenWhileItBuilds(t, vendor, releases)

		if result, _ := deploy(t, client, inlinePostgresRequest(server.url(), "17")); result.GetSuccess() {
			t.Fatal("Deploy() succeeded, want it overtaken at promotion")
		}
		if resolved := resolvedOrders(t, vendor); !strings.Contains(string(resolved.Value), "live.example") {
			t.Errorf("record = %q, want the live release's record put back", resolved.Value)
		}
	})
}

func TestAnInlineRecordAnotherDeployRepublishedIsNeitherOverwrittenNorPruned(t *testing.T) {
	t.Run("the carried record is refused rather than written over the other deploy's", func(t *testing.T) {
		builtProject(t)
		client, vendor := deployServed(t)
		server := servePostgres(t, "170004", 0)
		publishRecord(t, vendor, environment.TierProduction, naming.InlineRecordOwner, inlineOrdersAt("postgres://first.example/orders"))
		var raced sync.Once
		vendor.FakeStacks().Entering(func(provider.StackSpec) error {
			raced.Do(func() {
				publishRecord(t, vendor, environment.TierProduction, naming.InlineRecordOwner, inlineOrdersAt("postgres://racing.example/orders"))
			})
			return nil
		})

		result, _ := deploy(t, client, inlinePostgresRequest(server.url(), "17"))
		if result.GetSuccess() {
			t.Fatal("Deploy() succeeded, want it refused: the record moved since the deploy read it")
		}
		if resolved := resolvedOrders(t, vendor); !strings.Contains(string(resolved.Value), "racing.example") || resolved.Version != 2 {
			t.Errorf("record = %q v%d, want the racing deploy's write kept", resolved.Value, resolved.Version)
		}
	})

	t.Run("a record republished while the deploy ran is not pruned", func(t *testing.T) {
		builtProject(t)
		client, vendor := deployServed(t)
		server := servePostgres(t, "170004", 0)
		archive := naming.InlineRecordName(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "archive")
		publishRecord(t, vendor, environment.TierProduction, naming.InlineRecordOwner, postgresRecord(archive, "ocel.json"))
		var raced sync.Once
		vendor.FakeStacks().Entering(func(provider.StackSpec) error {
			raced.Do(func() {
				publishRecord(t, vendor, environment.TierProduction, naming.InlineRecordOwner, postgresRecord(archive, "ocel.json"))
			})
			return nil
		})

		if result, _ := deploy(t, client, inlinePostgresRequest(server.url(), "17")); !result.GetSuccess() {
			t.Fatalf("Deploy() = %q", result.GetError())
		}
		if _, kept := storedBindings(t, vendor)[archive]; !kept {
			t.Errorf("records = %v, want %s kept: another deploy republished it after this one read it", storedBindings(t, vendor), archive)
		}
	})
}
