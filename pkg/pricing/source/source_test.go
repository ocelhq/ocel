package source_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/pricing/source"
	"github.com/ocelhq/ocel/pkg/pricing/source/pulumi"
	"github.com/ocelhq/ocel/pkg/pricing/source/terraform"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func fixture(t *testing.T, path ...string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(path...))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReadDetectsEachFormatByContent(t *testing.T) {
	for _, c := range []struct {
		path        []string
		source      string
		hasPrior    bool
		wantSet     func([]byte) (*costv1.ResourceSet, *costv1.ResourceSet, error)
		assumptions []string
	}{
		{path: []string{"pulumi", "testdata", "preview-create.json"}, source: "pulumi", hasPrior: true, wantSet: withoutAssumptions(pulumi.ReadPreview)},
		{path: []string{"pulumi", "testdata", "preview-update.json"}, source: "pulumi", hasPrior: true, wantSet: withoutAssumptions(pulumi.ReadPreview)},
		{path: []string{"pulumi", "testdata", "sst-diff.json"}, source: "sst", hasPrior: true, wantSet: withoutAssumptions(pulumi.ReadSteps), assumptions: []string{pulumi.AssumptionSSTDiff}},
		{path: []string{"pulumi", "testdata", "export.json"}, source: "pulumi", wantSet: withoutPrior(pulumi.ReadExport)},
		{path: []string{"pulumi", "testdata", "sst-export.json"}, source: "sst", wantSet: withoutPrior(pulumi.ReadExport)},
		{path: []string{"terraform", "testdata", "plan-create.json"}, source: "terraform", hasPrior: true, wantSet: terraform.ReadPlan},
		{path: []string{"terraform", "testdata", "plan-update.json"}, source: "terraform", hasPrior: true, wantSet: terraform.ReadPlan},
		{path: []string{"terraform", "testdata", "state.json"}, source: "terraform", wantSet: withoutPrior(terraform.ReadState)},
	} {
		name := filepath.Join(c.path...)
		raw := fixture(t, c.path...)
		doc, err := source.Read(raw)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if doc.Set.GetSource() != c.source {
			t.Errorf("%s: source = %q, want %q", name, doc.Set.GetSource(), c.source)
		}
		if (doc.Prior != nil) != c.hasPrior {
			t.Errorf("%s: prior = %v, want one: %v", name, doc.Prior, c.hasPrior)
		}
		if doc.Prior != nil && doc.Prior.GetSource() != c.source {
			t.Errorf("%s: prior source = %q, want %q", name, doc.Prior.GetSource(), c.source)
		}
		if !slices.Equal(doc.Assumptions, c.assumptions) {
			t.Errorf("%s: assumptions = %v, want %v", name, doc.Assumptions, c.assumptions)
		}
		set, prior, err := c.wantSet(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(doc.Set, set) || !proto.Equal(doc.Prior, prior) {
			t.Errorf("%s: Read differs from the format's own reader", name)
		}
	}
}

func withoutAssumptions(read func([]byte) (*costv1.ResourceSet, *costv1.ResourceSet, []string, error)) func([]byte) (*costv1.ResourceSet, *costv1.ResourceSet, error) {
	return func(raw []byte) (*costv1.ResourceSet, *costv1.ResourceSet, error) {
		set, prior, _, err := read(raw)
		return set, prior, err
	}
}

func withoutPrior(read func([]byte) (*costv1.ResourceSet, error)) func([]byte) (*costv1.ResourceSet, *costv1.ResourceSet, error) {
	return func(raw []byte) (*costv1.ResourceSet, *costv1.ResourceSet, error) {
		set, err := read(raw)
		return set, nil, err
	}
}

func TestReadTakesTheResourceSetOfAScanResult(t *testing.T) {
	doc, err := source.Read(fixture(t, "testdata", "scan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Prior != nil {
		t.Errorf("prior = %v, want none", doc.Prior)
	}
	if doc.Set.GetSource() != "ocel" || len(doc.Set.GetResources()) != 2 || len(doc.Set.GetScopes()) != 3 {
		t.Errorf("set = %v", doc.Set)
	}
	if doc.Set.GetResources()[0].GetProperties().GetFields()["memory_size"].GetNumberValue() != 1024 {
		t.Errorf("first resource = %v", doc.Set.GetResources()[0])
	}
}

func TestReadRefusesAScanResultOutsideTheEnvelope(t *testing.T) {
	_, err := source.Read([]byte(`{"resources":{"source":"ocel","scopes":[{"id":"project:shop","kind":"project","name":"shop"}],
		"resources":[{"id":"r","scope":"project:shop","vendor":"aws","type":"aws_sqs_queue","region":"us-east-1"}]},"estimate":{"currency":"USD"}}`))
	if !isInvalid(err) {
		t.Errorf("err = %v, want an invalid refusal", err)
	}
}

func TestReadRefusesAScanResultWhoseSetIsInvalid(t *testing.T) {
	for _, raw := range []string{
		`{"ok":true,"data":{"resources":{"source":"chef","resources":[{"id":"a","scope":"s","vendor":"aws","type":"aws_sqs_queue"}]}}}`,
		`{"ok":true,"data":{"resources":{"source":"ocel","resources":[{"id":"","scope":"s","vendor":"aws","type":"aws_sqs_queue"}]}}}`,
	} {
		if _, err := source.Read([]byte(raw)); !isInvalid(err) {
			t.Errorf("%s: err = %v, want an invalid refusal", raw, err)
		}
	}
}

func TestReadRefusesAScanWhoseScopesFormACycleNamingTheScope(t *testing.T) {
	for _, c := range []struct{ scopes, named string }{
		{`[{"id":"a","parent":"b","kind":"component"},{"id":"b","parent":"a","kind":"component"}]`, `"a"`},
		{`[{"id":"root","kind":"project"},{"id":"self","parent":"self","kind":"component"}]`, `"self"`},
	} {
		_, err := source.Read([]byte(`{"ok":true,"data":{"resources":{"source":"ocel","scopes":` + c.scopes + `,
			"resources":[{"id":"r","scope":"root","vendor":"aws","type":"aws_sqs_queue"}]}}}`))
		if !isInvalid(err) || !strings.Contains(err.Error(), c.named) {
			t.Errorf("%s: err = %v, want an invalid refusal naming %s", c.scopes, err, c.named)
		}
	}
}

func TestReadRefusesAnUnrecognizedDocumentNamingTheFormatsItAccepts(t *testing.T) {
	for _, raw := range []string{"", "not json", "{}", `"text"`, `{"format_version":"1.2"}`, `[1,2]`, `{"steps":"no"}`} {
		_, err := source.Read([]byte(raw))
		if !isInvalid(err) {
			t.Errorf("%q: err = %v, want an invalid refusal", raw, err)
			continue
		}
		for _, format := range []string{"pulumi preview --json", "pulumi stack export", "sst state export", "sst diff --json", "terraform show -json", "ocel cost scan --json"} {
			if !strings.Contains(err.Error(), format) {
				t.Errorf("%q: %q does not name %s", raw, err, format)
			}
		}
	}
}

func TestReadTakesAnEmptySSTDiffAsNoChange(t *testing.T) {
	doc, err := source.Read([]byte("[]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Set.GetSource() != "sst" || len(doc.Set.GetResources()) != 0 || doc.Prior == nil {
		t.Errorf("doc = %v", doc)
	}
}

func isInvalid(err error) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == refusal.CodeInvalid
}
