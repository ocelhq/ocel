package domain

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestAGlobalDomainCommandNeedsPreview(t *testing.T) {
	t.Parallel()

	if err := requirePreviewTier("ocel domain use", true); err != nil {
		t.Fatalf("requirePreviewTier(preview) = %v, want nil", err)
	}

	err := requirePreviewTier("ocel domain use", false)
	if err == nil {
		t.Fatal("requirePreviewTier(no --preview) = nil, want a refusal")
	}
	for _, want := range []string{"--preview", "preview-only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
}

func TestTheGlobalPreviewDomainIsASingleLeadingWildcard(t *testing.T) {
	t.Parallel()

	t.Run("a leading wildcard label yields the base domain", func(t *testing.T) {
		t.Parallel()

		for arg, want := range map[string]string{
			"*.preview.acme.com": "preview.acme.com",
			" *.ACME.com ":       "acme.com", //nolint:gocritic // the whitespace is what the parser trims
		} {
			got, err := globalPreviewBaseDomain(arg)
			if err != nil {
				t.Fatalf("globalPreviewBaseDomain(%q) err = %v", arg, err)
			}
			if got != want {
				t.Errorf("globalPreviewBaseDomain(%q) = %q, want %q", arg, got, want)
			}
		}
	})

	t.Run("anything but a single leading wildcard label is refused", func(t *testing.T) {
		t.Parallel()

		for _, arg := range []string{"acme.com", "*acme.com", "*.*.acme.com", "preview.*.acme.com", "*"} {
			if _, err := globalPreviewBaseDomain(arg); err == nil {
				t.Errorf("globalPreviewBaseDomain(%q) = nil error, want a refusal", arg)
			}
		}
	})
}

func TestTheBootstrapWideDomainCommandsRefuseWithoutPreview(t *testing.T) {
	t.Run("the bootstrap-wide subcommands refuse without --preview", func(t *testing.T) {
		project := previewProject(t)
		invocation := newTestInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		runs := map[string]error{
			"use":     runDomainUse(context.Background(), invocation, project.Root, "*.preview.acme.com", domainOptions{}, &stdout),
			"release": runDomainRelease(context.Background(), invocation, project.Root, domainOptions{}, &stdout, strings.NewReader("")),
		}
		for name, err := range runs {
			if err == nil {
				t.Errorf("ocel domain %s without --preview = nil, want a refusal", name)
				continue
			}
			if !strings.Contains(err.Error(), "preview-only") {
				t.Errorf("ocel domain %s err = %v, want it to say global domains are preview-only", name, err)
			}
		}
	})
}
