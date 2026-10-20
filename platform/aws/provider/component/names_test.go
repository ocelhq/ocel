package component

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

func TestAnAppsExecutionRoleIsNamedForItsReleaseUnderTheAppFence(t *testing.T) {
	t.Parallel()

	coord := AppRoleCoordinate("shop", testStack(t, "prod", "web"))

	got, err := PhysicalName("ocel", coord, MaxRoleNameLen)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-shop-prod-web-role-r3f8a1c90"; got != want {
		t.Errorf("PhysicalName = %q, want %q", got, want)
	}
	if got, want := string(Describe(coord, "execution role for this app's functions")),
		"shop / prod / web - execution role for this app's functions - release r3f8a1c90"; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}
}

func TestAResourceOutsideAnyReleaseEndsInTheHashOfItsCoordinate(t *testing.T) {
	t.Parallel()

	worker := naming.Coordinate{Project: "shop", Env: "prod", App: "web", Kind: naming.KindWorker, Name: "mailer"}

	got, err := PhysicalName("ocel", worker, MaxLambdaNameLen)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-shop-prod-web-mailer-" + worker.HashTag("ocel"); got != want {
		t.Errorf("PhysicalName = %q, want %q", got, want)
	}
}

func TestANameThatCannotFitIsRefusedNamingTheResourceAndTheLimit(t *testing.T) {
	t.Parallel()

	at := naming.Coordinate{Project: "shop", Env: "prod", App: "web", Kind: naming.KindWorker, Name: strings.Repeat("m", 60)}

	_, err := PhysicalName("ocel", at, MaxLambdaNameLen)
	if err == nil {
		t.Fatal("a worker whose declared name alone overruns Lambda's limit was named rather than refused")
	}
	if !strings.Contains(err.Error(), "worker "+at.Name) || !strings.Contains(err.Error(), "64") {
		t.Errorf("refusal %q, want it to name the worker and Lambda's 64 characters", err)
	}
}

func TestAChildIsItsParentPlusItsSuffixAndAPartIsNamedUnderIt(t *testing.T) {
	t.Parallel()

	bucket := NameOf("ocel-app-shop-prod-uploads-k3x9q2")
	completer := bucket.Child("completer")

	if completer.Pulumi != "ocel-app-shop-prod-uploads-k3x9q2-completer" {
		t.Errorf("Child = %q, want the parent plus -completer", completer.Pulumi)
	}
	if got, want := bucket.Part("abac"), "ocel-app-shop-prod-uploads-k3x9q2/abac"; got != want {
		t.Errorf("Part = %q, want %q", got, want)
	}
	if got, want := completer.Attachment(iam.LambdaBasicExecutionPolicyARN), "ocel-app-shop-prod-uploads-k3x9q2-completer/AWSLambdaBasicExecutionRole"; got != want {
		t.Errorf("Attachment = %q, want %q", got, want)
	}
	if got, want := LambdaLogGroupName(completer).Pulumi, "/aws/lambda/ocel-app-shop-prod-uploads-k3x9q2-completer"; got != want {
		t.Errorf("LambdaLogGroupName = %q, want %q", got, want)
	}
}

func TestARandomlyTaggedNamesHeadLeavesRoomForTheTagAndItsChildren(t *testing.T) {
	t.Parallel()

	at := ResourceCoordinate("shop", "prod", "bucket--uploads", naming.KindBucket)
	head, err := RandomTaggedHead("ocel", at, 63, "completer")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-shop-prod-uploads"; head != want {
		t.Errorf("RandomTaggedHead = %q, want %q", head, want)
	}

	long := ResourceCoordinate(strings.Repeat("p", 30), strings.Repeat("e", 30), "bucket--uploads", naming.KindBucket)
	head, err = RandomTaggedHead("ocel", long, 63, "completer")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(head + "-" + naming.NewRandomTag() + "-completer"); got > 63 {
		t.Errorf("RandomTaggedHead = %q leaves its completer %d characters, over S3's 63", head, got)
	}
}

func fixedRelease(t *testing.T) naming.ReleaseToken {
	t.Helper()
	release, err := naming.ParseReleaseToken("r3f8a1c90")
	if err != nil {
		t.Fatalf("parse release: %v", err)
	}
	return release
}

func testStack(t *testing.T, env, app string) naming.StackName {
	t.Helper()
	return naming.AppStack(env, app, fixedRelease(t))
}
