package variablestoreserver_test

import (
	"context"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
)

const (
	infraPublisher = "urn:pulumi:prod::infra"
	otherPublisher = "urn:pulumi:prod::network"
)

func publish(variables variablestorev1connect.VariableStoreServiceClient, publisher string, values ...*variablestorev1.PublishedValue) (*variablestorev1.PublishValuesResponse, error) {
	return variables.PublishValues(context.Background(), &variablestorev1.PublishValuesRequest{
		Slug:      slug,
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		Publisher: publisher,
		Published: &variablestorev1.PublishedValues{Values: values},
	})
}

func unpublish(variables variablestorev1connect.VariableStoreServiceClient, publisher string, written ...*variablestorev1.ValueMetadata) (*variablestorev1.UnpublishValuesResponse, error) {
	versions := &variablestorev1.PublishedVersions{}
	for _, value := range written {
		versions.Values = append(versions.Values, &variablestorev1.PublishedVersion{Key: value.GetCoordinate().GetKey(), Version: value.GetVersion()})
	}
	return variables.UnpublishValues(context.Background(), &variablestorev1.UnpublishValuesRequest{
		Slug:      slug,
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		Publisher: publisher,
		Published: versions,
	})
}

func published(key, value string, secret bool) *variablestorev1.PublishedValue {
	return &variablestorev1.PublishedValue{Key: key, Value: value, Secret: secret}
}

func listedByKey(t *testing.T, variables variablestorev1connect.VariableStoreServiceClient) map[string]*variablestorev1.ValueMetadata {
	t.Helper()
	listed, err := variables.ListValues(context.Background(), &variablestorev1.ListValuesRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Slug: slug})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*variablestorev1.ValueMetadata{}
	for _, value := range listed.GetValues() {
		out[value.GetCoordinate().GetKey()] = value
	}
	return out
}

func TestAPublishBeforeTheProjectsFirstDeployIsKeptWithItsPublisher(t *testing.T) {
	variables, _ := served(t)

	if _, err := publish(variables, infraPublisher, published("API_KEY", "k3y", true), published("DATABASE_URL", "postgres://db", false)); err != nil {
		t.Fatalf("PublishValues() before any deploy = %v, want it kept", err)
	}

	listed := listedByKey(t, variables)
	if key := listed["API_KEY"]; key.GetPublisher() != infraPublisher || !key.GetPublishedSecret() {
		t.Errorf("API_KEY = %+v, want it published secret by %s", key, infraPublisher)
	}
	if url := listed["DATABASE_URL"]; url.GetPublisher() != infraPublisher || url.GetPublishedSecret() {
		t.Errorf("DATABASE_URL = %+v, want it published plain by %s", url, infraPublisher)
	}
	got, err := revealOne(context.Background(), variables, environmentv1.Tier_TIER_PRODUCTION, cell("API_KEY"))
	if err != nil || got.GetValue() != "k3y" {
		t.Fatalf("GetValue(API_KEY) = %q, %v, want the published value", got.GetValue(), err)
	}
}

func TestARepublishRewritesItsValuesAndRemovesTheOnesItNoLongerLists(t *testing.T) {
	variables, _ := served(t)
	if err := setValue(t, variables, environmentv1.Tier_TIER_PRODUCTION, cell("LOG_LEVEL"), "debug"); err != nil {
		t.Fatal(err)
	}
	if _, err := publish(variables, infraPublisher, published("API_KEY", "one", true), published("DATABASE_URL", "postgres://db", false)); err != nil {
		t.Fatal(err)
	}

	again, err := publish(variables, infraPublisher, published("API_KEY", "two", true))
	if err != nil {
		t.Fatal(err)
	}
	if removed := again.GetRemoved(); len(removed) != 1 || removed[0].GetKey() != "DATABASE_URL" {
		t.Errorf("removed = %v, want DATABASE_URL, which the publisher stopped listing", removed)
	}
	listed := listedByKey(t, variables)
	if _, kept := listed["DATABASE_URL"]; kept {
		t.Error("DATABASE_URL is still set after its publisher stopped listing it")
	}
	if _, kept := listed["LOG_LEVEL"]; !kept {
		t.Error("LOG_LEVEL, set by hand, was removed by a publisher that never wrote it")
	}
	got, err := revealOne(context.Background(), variables, environmentv1.Tier_TIER_PRODUCTION, cell("API_KEY"))
	if err != nil || got.GetValue() != "two" {
		t.Fatalf("GetValue(API_KEY) = %q, %v, want the republished value", got.GetValue(), err)
	}

	if _, err := publish(variables, infraPublisher); err != nil {
		t.Fatal(err)
	}
	listed = listedByKey(t, variables)
	if _, kept := listed["API_KEY"]; kept || len(listed) != 1 {
		t.Errorf("values after publishing nothing = %v, want only the hand-set LOG_LEVEL", listed)
	}
}

func TestAHandWriteOverAPublishedValueIsRefusedNamingWhereToChangeIt(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()
	if _, err := publish(variables, infraPublisher, published("API_KEY", "k3y", true)); err != nil {
		t.Fatal(err)
	}
	if err := setValue(t, variables, environmentv1.Tier_TIER_PRODUCTION, cell("SHARED_KEY"), "elsewhere"); err != nil {
		t.Fatal(err)
	}

	for name, write := range map[string]func() error{
		"SetValue": func() error {
			return setValue(t, variables, environmentv1.Tier_TIER_PRODUCTION, cell("API_KEY"), "by hand")
		},
		"DeleteValue": func() error {
			_, err := variables.DeleteValue(ctx, &variablestorev1.DeleteValueRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Coordinate: cell("API_KEY")})
			return err
		},
		"SetReference": func() error {
			_, err := variables.SetReference(ctx, &variablestorev1.SetReferenceRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Coordinate: cell("API_KEY"), Target: cell("SHARED_KEY")})
			return err
		},
	} {
		err := write()
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), infraPublisher) || !strings.Contains(err.Error(), "change it in") {
			t.Errorf("%s over a published value = %v, want FailedPrecondition naming %s and where to change it", name, err, infraPublisher)
		}
	}
}

func TestAPublishOverAValueAnotherPublisherWroteIsRefusedAndWritesNothing(t *testing.T) {
	variables, _ := served(t)
	if _, err := publish(variables, otherPublisher, published("API_KEY", "theirs", true)); err != nil {
		t.Fatal(err)
	}

	_, err := publish(variables, infraPublisher, published("DATABASE_URL", "postgres://db", false), published("API_KEY", "mine", true))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), otherPublisher) {
		t.Fatalf("PublishValues() over another publisher's value = %v, want FailedPrecondition naming %s", err, otherPublisher)
	}
	if _, written := listedByKey(t, variables)["DATABASE_URL"]; written {
		t.Error("DATABASE_URL was written by a publish that was refused")
	}
}

func TestAPublishToAProjectWhoseValuesAnEnvSourceOwnsIsRefused(t *testing.T) {
	variables, _ := served(t)
	exec := execSource(t, &variablestorev1.EnvSourceValue{Cell: &variablestorev1.Cell{Key: "TOKEN"}, Value: "t"})
	if _, err := syncEnvSource(variables, environmentv1.Tier_TIER_PRODUCTION, exec); err != nil {
		t.Fatal(err)
	}

	_, err := publish(variables, infraPublisher, published("API_KEY", "k3y", true))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "exec") {
		t.Fatalf("PublishValues() into an env source's values = %v, want FailedPrecondition naming the env source", err)
	}
}

func TestAPublishWithAValueTooLargeToKeepIsRefusedAndWritesNothing(t *testing.T) {
	variables, _ := served(t)

	_, err := publish(variables, infraPublisher, published("API_KEY", "k3y", true), published("CERTIFICATE", strings.Repeat("x", 4097), false))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "CERTIFICATE") {
		t.Fatalf("PublishValues() with a 4097-byte CERTIFICATE = %v, want InvalidArgument naming CERTIFICATE", err)
	}
	if _, written := listedByKey(t, variables)["API_KEY"]; written {
		t.Error("API_KEY was written by a publish that was refused")
	}
}

func TestAPublishNamingOneKeyTwiceIsRefused(t *testing.T) {
	variables, _ := served(t)

	_, err := publish(variables, infraPublisher, published("API_KEY", "one", true), published("API_KEY", "two", true))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("PublishValues() naming API_KEY twice = %v, want InvalidArgument", err)
	}
}

func TestUnpublishingRemovesTheValuesStillAtTheVersionsItWrote(t *testing.T) {
	variables, _ := served(t)
	wrote, err := publish(variables, infraPublisher, published("API_KEY", "k3y", true), published("DATABASE_URL", "postgres://db", false))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := unpublish(variables, infraPublisher, wrote.GetWritten()...)
	if err != nil {
		t.Fatalf("UnpublishValues() = %v, want the values it wrote removed", err)
	}
	if removed := resp.GetRemoved(); len(removed) != 2 {
		t.Errorf("removed = %v, want API_KEY and DATABASE_URL", removed)
	}
	if listed := listedByKey(t, variables); len(listed) != 0 {
		t.Errorf("values after unpublishing = %v, want none", listed)
	}
}

func TestUnpublishingAValueTheProgramWroteAgainSinceLeavesItInPlace(t *testing.T) {
	variables, _ := served(t)
	before, err := publish(variables, infraPublisher, published("API_KEY", "k3y", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publish(variables, infraPublisher, published("API_KEY", "k3y", true)); err != nil {
		t.Fatalf("PublishValues() by the same program from a moved call = %v, want it written", err)
	}

	resp, err := unpublish(variables, infraPublisher, before.GetWritten()...)
	if err != nil {
		t.Fatalf("UnpublishValues() of a version since rewritten = %v, want it to leave the value", err)
	}
	if removed := resp.GetRemoved(); len(removed) != 0 {
		t.Errorf("removed = %v, want nothing: the value was written again since", removed)
	}
	got, err := revealOne(context.Background(), variables, environmentv1.Tier_TIER_PRODUCTION, cell("API_KEY"))
	if err != nil || got.GetValue() != "k3y" || got.GetMetadata().GetPublisher() != infraPublisher {
		t.Fatalf("GetValue(API_KEY) = %q by %q, %v, want the rewritten value kept", got.GetValue(), got.GetMetadata().GetPublisher(), err)
	}
}
