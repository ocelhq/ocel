package projectinit

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/configdoc"
)

const optionFlag = "--option"

func parseOptionFlags(flags []string) ([]providerSetting, error) {
	settings := make([]providerSetting, 0, len(flags))
	for _, flag := range flags {
		name, value, spelled := strings.Cut(flag, "=")
		name = strings.TrimSpace(name)
		if !spelled || name == "" {
			return nil, fmt.Errorf("%s names %q, and an option is spelled name=value", optionFlag, flag)
		}
		if slices.ContainsFunc(settings, func(set providerSetting) bool { return set.name == name }) {
			return nil, fmt.Errorf("%s gives %q twice — give each option once", optionFlag, name)
		}
		settings = append(settings, providerSetting{name: name, value: value})
	}
	return settings, nil
}

func findMissingOptions(provider string, settings []providerSetting) []configdoc.ProviderOption {
	return slices.DeleteFunc(configdoc.RequiredProviderOptions(provider), func(required configdoc.ProviderOption) bool {
		return slices.ContainsFunc(settings, func(set providerSetting) bool {
			return set.name == required.Name && set.value != ""
		})
	})
}

func refuseMissingOptions(provider string, settings []providerSetting) error {
	missing := findMissingOptions(provider, settings)
	if len(missing) == 0 {
		return nil
	}
	names := make([]string, len(missing))
	typed := make([]string, len(missing))
	for i, option := range missing {
		names[i] = option.Name
		typed[i] = fmt.Sprintf("%s %s=<value>", optionFlag, option.Name)
	}
	return clierror.NewInputRequired(
		fmt.Errorf("%s cannot deploy without %s — give each with `%s`", provider, strings.Join(names, ", "), optionFlag+" name=value"),
		strings.Join(typed, " "),
	)
}

func askMissingOptions(ctx context.Context, prompt terminal.Prompt, provider string, settings []providerSetting) ([]providerSetting, bool, error) {
	for _, required := range findMissingOptions(provider, settings) {
		value, answered, err := prompt.Input(ctx, required.Name, required.Doc)
		if err != nil || !answered || value == "" {
			return settings, false, err
		}
		settings = append(slices.DeleteFunc(settings, func(set providerSetting) bool { return set.name == required.Name }), providerSetting{name: required.Name, value: value})
	}
	return settings, true, nil
}
