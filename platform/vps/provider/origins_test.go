package vps_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
)

func lastFedTo(t *testing.T, machine *box, needle string) (string, string) {
	t.Helper()
	machine.mu.Lock()
	defer machine.mu.Unlock()
	for i := len(machine.ran) - 1; i >= 0; i-- {
		if !strings.Contains(machine.ran[i], needle) {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(machine.fed[i])
		if err != nil {
			t.Fatalf("what is fed to the store is not what the box decodes: %v", err)
		}
		return machine.ran[i], string(decoded)
	}
	t.Fatalf("nothing on the box ever reached %s", needle)
	return "", ""
}

func stoodWithABucket(t *testing.T, declared ...string) (*box, *vps.Provider, edge.EdgeStack) {
	t.Helper()

	ctx := context.Background()
	machine := &box{kept: sealedRootKey()}
	p := over(machine)
	records := fake.NewRecords()
	p.Recording(records)

	bucket := aBucket(t, "uploads", false)
	bucket.Resource.Bucket.AllowedOrigins = declared
	binding, err := p.Bucket(ctx, bucket, nil)
	if err != nil {
		t.Fatalf("Bucket() = %v", err)
	}
	if err := providerkit.WriteStack(ctx, records, providerkit.ClassProduction, "shop",
		naming.InfraStack(bucket.Ref.Name.Env), providerkit.Stack{
			Kind: providerkit.StackInfra, Bindings: []providerkit.Binding{binding},
		}); err != nil {
		t.Fatal(err)
	}

	front, err := p.Edges().Open(boxedge.Kind)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := front.Reconcile(ctx, edge.StackSpec{
		Version: "test", Class: edge.ClassProduction, Slug: "shop",
	}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	return machine, p, stack
}

func TestAHostnameBoundAfterTheStoreStoodUpIsAnOriginItsBucketAnswers(t *testing.T) {
	t.Parallel()

	machine, _, stack := stoodWithABucket(t, "https://app.example.com")
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain() = %v", err)
	}

	_, held := lastFedTo(t, machine, "?cors")
	for _, origin := range []string{"https://shop.example.com", "https://app.example.com"} {
		if !strings.Contains(held, "<AllowedOrigin>"+origin+"</AllowedOrigin>") {
			t.Errorf("after the bind the bucket answers\n%s\nand not %s: a first deploy stands its store up before it binds its hostnames, so a browser on the project's own hostname is refused until the next deploy", held, origin)
		}
	}
}

func TestAnUnboundHostnameIsNoLongerAnOriginItsBucketAnswers(t *testing.T) {
	t.Parallel()

	machine, _, stack := stoodWithABucket(t, "https://app.example.com")
	ctx := context.Background()
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain() = %v", err)
	}
	if err := stack.UnbindDomain(ctx, "shop.example.com"); err != nil {
		t.Fatalf("UnbindDomain() = %v", err)
	}

	_, held := lastFedTo(t, machine, "?cors")
	if strings.Contains(held, "https://shop.example.com") {
		t.Errorf("after the unbind the bucket still answers shop.example.com:\n%s", held)
	}
	if !strings.Contains(held, "<AllowedOrigin>https://app.example.com</AllowedOrigin>") {
		t.Errorf("the unbind took the origin the bucket declared with it:\n%s", held)
	}
}

func TestABucketLeftWithNoOriginAnswersNoBrowserAtAll(t *testing.T) {
	t.Parallel()

	machine, _, stack := stoodWithABucket(t)
	ctx := context.Background()
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain() = %v", err)
	}
	if err := stack.UnbindDomain(ctx, "shop.example.com"); err != nil {
		t.Fatalf("UnbindDomain() = %v", err)
	}

	command, _ := lastFedTo(t, machine, "?cors")
	if !strings.Contains(command, "'DELETE'") {
		t.Errorf("the last call to the bucket's cors was\n%s\nwant it deleted: the one origin it answered was released, and a rule left standing keeps answering it", command)
	}
}
