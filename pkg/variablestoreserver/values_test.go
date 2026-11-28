package variablestoreserver_test

import (
	"context"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
)

func setClassed(t *testing.T, variables variablestorev1connect.VariableStoreServiceClient, key, value string, class resourcesv1.VariableClass) {
	t.Helper()
	_, err := variables.SetValues(context.Background(), &variablestorev1.SetValuesRequest{
		Tier:   environmentv1.Tier_TIER_PRODUCTION,
		Slug:   slug,
		Values: []*variablestorev1.ValueWrite{{Coordinate: cell(key), Value: value, Class: class}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAValueAnswersWithTheClassItWasWrittenAs(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()
	setClassed(t, variables, "REGION", "eu", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN)
	setClassed(t, variables, "API_KEY", "k3y", resourcesv1.VariableClass_VARIABLE_CLASS_UNSPECIFIED)

	got, err := variables.GetValue(ctx, &variablestorev1.GetValueRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Coordinate: cell("REGION")})
	if err != nil || got.GetMetadata().GetClass() != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
		t.Fatalf("GetValue() = %v, %v, want REGION answered as plain", got.GetMetadata(), err)
	}
	listed, err := variables.ListValues(ctx, &variablestorev1.ListValuesRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Slug: slug})
	if err != nil {
		t.Fatal(err)
	}
	classes := map[string]resourcesv1.VariableClass{}
	for _, value := range listed.GetValues() {
		classes[value.GetCoordinate().GetKey()] = value.GetClass()
	}
	if classes["REGION"] != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN || classes["API_KEY"] != resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE {
		t.Fatalf("ListValues() classes = %v, want REGION plain and API_KEY, written with no class, sensitive", classes)
	}
}

func TestRevealValuesRefusesASecretAndRevealsNothingBesideIt(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()
	setClassed(t, variables, "REGION", "eu", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN)
	setClassed(t, variables, "API_KEY", "k3y", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET)

	revealed, err := variables.RevealValues(ctx, &variablestorev1.RevealValuesRequest{
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Slug:  slug,
		Cells: []*variablestorev1.Coordinate{cell("REGION"), cell("API_KEY")},
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "API_KEY") {
		t.Fatalf("RevealValues() over a secret = %v, %v, want a refusal naming API_KEY", revealed.GetValues(), err)
	}
	if strings.Contains(err.Error(), "k3y") {
		t.Fatalf("the refusal carries the secret's plaintext: %v", err)
	}
}

func TestRevealValuesRevealsPlainAndSensitiveValues(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()
	setClassed(t, variables, "REGION", "eu", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN)
	setClassed(t, variables, "WEBHOOK", "https://hooks.example/abc", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE)

	revealed, err := variables.RevealValues(ctx, &variablestorev1.RevealValuesRequest{
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Slug:  slug,
		Cells: []*variablestorev1.Coordinate{cell("REGION"), cell("WEBHOOK")},
	})
	if err != nil || len(revealed.GetValues()) != 2 {
		t.Fatalf("RevealValues() = %v, %v, want both values", revealed.GetValues(), err)
	}
}

func TestDeliverValuesAnswersASecretForTheDeployThatReadsIt(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()
	setClassed(t, variables, "API_KEY", "k3y", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET)

	delivered, err := variables.DeliverValues(ctx, &variablestorev1.DeliverValuesRequest{
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Slug:  slug,
		Cells: []*variablestorev1.Coordinate{cell("API_KEY"), cell("NOTHING_HERE")},
	})
	if err != nil || len(delivered.GetValues()) != 1 || delivered.GetValues()[0].GetValue() != "k3y" {
		t.Fatalf("DeliverValues() = %v, %v, want API_KEY's value alone", delivered.GetValues(), err)
	}
	if delivered.GetValues()[0].GetMetadata().GetClass() != resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
		t.Fatalf("DeliverValues() answered %v, want it marked secret", delivered.GetValues()[0].GetMetadata())
	}
}

func TestClassifyValuesMakesADeclaredSecretUnrevealable(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()
	setClassed(t, variables, "API_KEY", "k3y", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE)

	classified, err := variables.ClassifyValues(ctx, &variablestorev1.ClassifyValuesRequest{
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Slug:  slug,
		Cells: []*variablestorev1.ClassifiedCell{{Key: "API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET}},
	})
	if err != nil || len(classified.GetRaised()) != 1 {
		t.Fatalf("ClassifyValues() = %v, %v, want API_KEY raised", classified.GetRaised(), err)
	}
	_, err = variables.RevealValues(ctx, &variablestorev1.RevealValuesRequest{
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Slug:  slug,
		Cells: []*variablestorev1.Coordinate{cell("API_KEY")},
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("RevealValues() of a value its deploy declared secret = %v, want it refused", err)
	}
}

func TestClassifyValuesRefusesACellWithNoClass(t *testing.T) {
	variables, _ := served(t)

	_, err := variables.ClassifyValues(context.Background(), &variablestorev1.ClassifyValuesRequest{
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Slug:  slug,
		Cells: []*variablestorev1.ClassifiedCell{{Key: "API_KEY"}},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("ClassifyValues() with no class = %v, want an invalid argument", err)
	}
}
