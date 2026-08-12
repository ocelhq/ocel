package bootstrap

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

func TestPreviewDomainParam(t *testing.T) {
	t.Parallel()

	t.Run("round-trips the record as a SecureString", func(t *testing.T) {
		t.Parallel()
		fake := newFakeSSM()
		want := PreviewDomain{BaseDomain: "preview.acme.com", CloudflareAccount: "acct", GrammarMin: 1, GrammarMax: 1}

		if err := WritePreviewDomain(context.Background(), fake, ClassPreview, want); err != nil {
			t.Fatalf("WritePreviewDomain: %v", err)
		}
		if !strings.Contains(fake.params[PreviewDomainParamName], "preview.acme.com") {
			t.Fatalf("param = %q", fake.params[PreviewDomainParamName])
		}
		got, err := ReadPreviewDomain(context.Background(), fake, ClassPreview)
		if err != nil {
			t.Fatalf("ReadPreviewDomain: %v", err)
		}
		if got != want {
			t.Errorf("ReadPreviewDomain = %+v, want %+v", got, want)
		}
	})

	t.Run("absence is a normal state", func(t *testing.T) {
		t.Parallel()
		got, err := ReadPreviewDomain(context.Background(), newFakeSSM(), ClassPreview)
		if err != nil {
			t.Fatalf("ReadPreviewDomain: %v", err)
		}
		if got != (PreviewDomain{}) {
			t.Errorf("ReadPreviewDomain = %+v, want the zero record", got)
		}
	})

	t.Run("deleting what is not there succeeds", func(t *testing.T) {
		t.Parallel()
		if err := DeletePreviewDomain(context.Background(), newFakeSSM(), ClassPreview); err != nil {
			t.Fatalf("DeletePreviewDomain: %v", err)
		}
	})

	t.Run("production has no preview domain", func(t *testing.T) {
		t.Parallel()
		for _, err := range []error{
			func() error {
				_, err := ReadPreviewDomain(context.Background(), newFakeSSM(), ClassProduction)
				return err
			}(),
			WritePreviewDomain(context.Background(), newFakeSSM(), ClassProduction, PreviewDomain{}),
			DeletePreviewDomain(context.Background(), newFakeSSM(), ClassProduction),
		} {
			if err == nil {
				t.Fatal("expected the production class to be refused")
			}
		}
	})
}

func TestReadClassParamsPreviewDomain(t *testing.T) {
	t.Parallel()

	t.Run("comes back with the batched read", func(t *testing.T) {
		t.Parallel()
		fake := &fakeBatchSSM{params: map[string]string{
			PassphraseParamName:    "pass",
			PreviewDomainParamName: `{"baseDomain":"preview.acme.com","cloudflareAccount":"acct","grammarMin":1,"grammarMax":1}`,
		}}

		params, err := ReadClassParams(context.Background(), fake, ClassPreview, "proj")
		if err != nil {
			t.Fatalf("ReadClassParams: %v", err)
		}
		if params.PreviewDomain.BaseDomain != "preview.acme.com" || params.PreviewDomain.CloudflareAccount != "acct" {
			t.Errorf("PreviewDomain = %+v", params.PreviewDomain)
		}
		if fake.calls != 1 {
			t.Errorf("calls = %d, want 1: the preview domain rides the existing round trip", fake.calls)
		}
	})

	t.Run("production never asks for it", func(t *testing.T) {
		t.Parallel()
		fake := &fakeBatchSSM{params: map[string]string{PassphraseParamName: "pass"}}

		if _, err := ReadClassParams(context.Background(), fake, ClassProduction, "proj"); err != nil {
			t.Fatalf("ReadClassParams: %v", err)
		}
		if slices.Contains(fake.requested, PreviewDomainParamName) {
			t.Errorf("requested = %v, want no preview domain", fake.requested)
		}
	})

	t.Run("absent leaves the zero record", func(t *testing.T) {
		t.Parallel()
		fake := &fakeBatchSSM{params: map[string]string{PassphraseParamName: "pass"}}

		params, err := ReadClassParams(context.Background(), fake, ClassPreview, "proj")
		if err != nil {
			t.Fatalf("ReadClassParams: %v", err)
		}
		if params.PreviewDomain != (PreviewDomain{}) {
			t.Errorf("PreviewDomain = %+v, want the zero record", params.PreviewDomain)
		}
	})
}

type fakePathSSM struct {
	names []string
	pages int
}

func (f *fakePathSSM) GetParametersByPath(_ context.Context, in *ssm.GetParametersByPathInput, _ ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error) {
	prefix := aws.ToString(in.Path)
	var matched []string
	for _, name := range f.names {
		if strings.HasPrefix(name, prefix) {
			matched = append(matched, name)
		}
	}
	out := &ssm.GetParametersByPathOutput{}
	start := 0
	if in.NextToken != nil {
		start = 1
	}
	for i, name := range matched {
		if i < start {
			continue
		}
		out.Parameters = append(out.Parameters, ssmtypes.Parameter{Name: aws.String(name)})
		if i == 0 && len(matched) > 1 {
			out.NextToken = aws.String("more")
			break
		}
	}
	f.pages++
	return out, nil
}

func TestRootStackSlugsFor(t *testing.T) {
	t.Parallel()

	fake := &fakePathSSM{names: []string{
		PreviewRootStackStateParamPrefix + "web",
		PreviewRootStackStateParamPrefix + "acme",
		RootStackStateParamPrefix + "other",
	}}

	slugs, err := RootStackSlugsFor(context.Background(), fake, ClassPreview)
	if err != nil {
		t.Fatalf("RootStackSlugsFor: %v", err)
	}
	if !slices.Equal(slugs, []string{"acme", "web"}) {
		t.Errorf("slugs = %v, want the preview class sorted", slugs)
	}
	if fake.pages != 2 {
		t.Errorf("pages = %d, want the pagination followed", fake.pages)
	}
}
