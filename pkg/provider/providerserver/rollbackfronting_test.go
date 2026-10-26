package providerserver_test

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	thirdAdminBuildID  = "0123456789abcdef0123456789abcdef"
	fourthAdminBuildID = "fedcba9876543210fedcba9876543210"
)

func skewLatestPromotion(t *testing.T, p *fake.Provider, by time.Duration) {
	t.Helper()
	ctx := context.Background()
	key := ledger.Partition(environment.TierProduction, "shop").Key("pointers", router.ResolvePointer(router.DefaultPointer))
	entry, err := p.KeyValues().Read(ctx, key)
	if err != nil {
		t.Fatalf("read the production pointer: %v", err)
	}
	var pointer ledger.Pointer
	if err := json.Unmarshal(entry.Value, &pointer); err != nil {
		t.Fatalf("decode the production pointer: %v", err)
	}
	pointer.Promotions[0].Ts += int64(by / time.Second)
	if entry.Value, err = json.Marshal(pointer); err != nil {
		t.Fatalf("encode the production pointer: %v", err)
	}
	if _, err := p.KeyValues().Write(ctx, entry); err != nil {
		t.Fatalf("write the production pointer: %v", err)
	}
}

func deployedReleases(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.DeployRequest) map[string]string {
	t.Helper()
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	return releasesOf(result)
}

func servedOn(p *fake.Provider, kind router.Kind) map[string]string {
	return p.Routers().(*fake.Routers).DataPlane(kind).Releases("shop", environment.TierProduction, router.DefaultPointer)
}

func refusedRollback(t *testing.T, client contractv1connect.ProviderServiceClient, p *fake.Provider, want string) {
	t.Helper()
	before := map[router.Kind]map[string]string{}
	for _, kind := range []router.Kind{fake.RouterRelay, fake.RouterDirect} {
		before[kind] = servedOn(p, kind)
	}
	rolled, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"})
	if err == nil {
		t.Fatalf("Rollback() promoted %s, want it refused", rolled.promotionID())
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("Rollback() error = %v, want it to say %q", err, want)
	}
	for _, kind := range []router.Kind{fake.RouterRelay, fake.RouterDirect} {
		if got := servedOn(p, kind); !maps.Equal(got, before[kind]) {
			t.Errorf("the %s router serves %v after the refused rollback, want %v: no pointer moves", kind, got, before[kind])
		}
	}
}

func TestARollbackToAPromotionDeployedBeforeTheEdgeFrontedTheProjectIsRefusedBeforeAnyPointerMoves(t *testing.T) {
	client, p, relay := frontedOnlyServed(t)
	deployedReleases(t, client, mixedRequest())
	relay.FrontsOrigins()
	deployedReleases(t, client, withContainerAdmin(deployRequest(), "abcdefabcdefabcdefabcdefabcdefab"))

	refusedRollback(t, client, p, "was deployed before the relay edge started fronting this project's apps")
}

func TestARollbackToAPromotionDeployedWhileTheEdgeFrontedTheProjectIsRefusedOnceItStops(t *testing.T) {
	client, p, relay := frontedOnlyServed(t)
	relay.FrontsOrigins()
	deployedReleases(t, client, mixedRequest())
	relay.FrontsNoOrigin()
	deployedReleases(t, client, withContainerAdmin(deployRequest(), "abcdefabcdefabcdefabcdefabcdefab"))

	refusedRollback(t, client, p, "was deployed before the relay edge stopped fronting this project's apps")
}

func TestARollbackUnderAnEdgeThatFrontsTheProjectOtherwiseThanItsLastDeployIsRefused(t *testing.T) {
	client, p, relay := frontedOnlyServed(t)
	relay.FrontsOrigins()
	deployedReleases(t, client, mixedRequest())
	deployedReleases(t, client, withContainerAdmin(deployRequest(), "abcdefabcdefabcdefabcdefabcdefab"))
	relay.FrontsNoOrigin()

	refusedRollback(t, client, p, "Deploy before rolling back")
}

func TestARollbackToThePromotionThatStartedTheEdgeFrontingTheProjectMovesIt(t *testing.T) {
	client, p, relay := frontedOnlyServed(t)
	deployedReleases(t, client, mixedRequest())
	relay.FrontsOrigins()
	fronted := deployedReleases(t, client, withContainerAdmin(deployRequest(), "abcdefabcdefabcdefabcdefabcdefab"))
	deployedReleases(t, client, withContainerAdmin(deployRequest(), thirdAdminBuildID))

	if _, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if got := servedOn(p, fake.RouterRelay); !maps.Equal(got, fronted) {
		t.Errorf("after the rollback the %s router serves %v, want %v", fake.RouterRelay, got, fronted)
	}
}

func TestARollbackToAPromotionDeployedBeforeTheEdgeFrontedTheProjectIsRefusedFromAMachineWhoseClockRanAhead(t *testing.T) {
	client, p, relay := frontedOnlyServed(t)
	deployedReleases(t, client, mixedRequest())
	skewLatestPromotion(t, p, time.Hour)
	relay.FrontsOrigins()
	deployedReleases(t, client, withContainerAdmin(deployRequest(), "abcdefabcdefabcdefabcdefabcdefab"))

	refusedRollback(t, client, p, "was deployed before the relay edge started fronting this project's apps")
}

func TestARollbackToAPromotionDeployedSinceTheEdgeFrontedTheProjectMovesItFromAMachineWhoseClockRanBehind(t *testing.T) {
	client, p, relay := frontedOnlyServed(t)
	deployedReleases(t, client, mixedRequest())
	relay.FrontsOrigins()
	deployedReleases(t, client, withContainerAdmin(deployRequest(), "abcdefabcdefabcdefabcdefabcdefab"))
	since := deployedReleases(t, client, withContainerAdmin(deployRequest(), thirdAdminBuildID))
	skewLatestPromotion(t, p, -time.Hour)
	deployedReleases(t, client, withContainerAdmin(deployRequest(), fourthAdminBuildID))

	if _, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if got := servedOn(p, fake.RouterRelay); !maps.Equal(got, since) {
		t.Errorf("after the rollback the %s router serves %v, want %v", fake.RouterRelay, got, since)
	}
}
