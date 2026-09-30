package domain

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
)

func NewSetup() prerequisite.Setup {
	return prerequisite.Setup{Run: runSetup}
}

func runSetup(ctx context.Context, policy consent.Policy, span *run.Span, missing prerequisite.MissingError) error {
	var unnamed readiness.NoHostnameError
	if !errors.As(missing, &unnamed) {
		return missing
	}
	var saved bool
	err := span.Ask(func() (err error) {
		snippet := hostnameSnippet(unnamed.ConfigPath, unnamed.Slug+".example.com")
		fmt.Fprintf(policy.Out, "Add this to %s:\n\n    %s\n\n", filepath.Base(unnamed.ConfigPath), strings.ReplaceAll(snippet, "\n", "\n    "))
		saved, err = terminal.NewPrompt(policy.Out, policy.In).AwaitEnter(ctx, "Press Enter once it's saved (or n to stop)")
		return err
	})
	if err != nil {
		return err
	}
	if !saved {
		return prerequisite.SetupDeclinedError{Missing: missing}
	}
	return nil
}

func hostnameSnippet(configPath, hostname string) string {
	switch {
	case project.IsTypeScript(configPath):
		return fmt.Sprintf("domains: { production: %q },", hostname)
	case project.IsYAML(configPath):
		return fmt.Sprintf("domains:\n  production: %s", hostname)
	}
	return fmt.Sprintf(`"domains": { "production": %q }`, hostname)
}
