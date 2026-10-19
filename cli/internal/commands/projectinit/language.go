package projectinit

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/language"
)

type sdkLanguage struct {
	name       string
	language   language.Language
	addCommand []string
	sdk        string
}

var sdkLanguages = []sdkLanguage{
	{name: "go", language: language.Go, addCommand: []string{"go", "get"}, sdk: goSDKModule},
	{name: "rust", language: language.Rust, addCommand: []string{"cargo", "add"}, sdk: rustSDKCrate},
	{name: "python", language: language.Python, addCommand: []string{"uv", "add"}, sdk: sdkPackage},
	{name: "node", language: language.JS, sdk: sdkPackage},
}

func languageNames() []string {
	names := make([]string, 0, len(sdkLanguages))
	for _, l := range sdkLanguages {
		names = append(names, l.name)
	}
	return names
}

func languageNamed(name string) (sdkLanguage, error) {
	for _, l := range sdkLanguages {
		if l.name == name {
			return l, nil
		}
	}
	return sdkLanguage{}, fmt.Errorf("%q is no language ocel ships an SDK for — name one of %s", name, strings.Join(languageNames(), ", "))
}

func sdkLanguageOf(written language.Language) sdkLanguage {
	for _, l := range sdkLanguages {
		if l.language == written {
			return l
		}
	}
	return sdkLanguage{}
}

func detectLanguage(dir string) (sdkLanguage, bool, error) {
	detected, err := language.Resolve(dir, dir, "")
	if err == nil {
		return sdkLanguageOf(detected.Language), true, nil
	}
	var undetected *language.UndetectedAppError
	if !errors.As(err, &undetected) {
		return sdkLanguage{}, false, err
	}
	written := undetected.Languages()
	switch len(written) {
	case 0:
		return sdkLanguage{}, false, nil
	case 1:
		return sdkLanguageOf(written[0]), true, nil
	}
	held := make([]string, 0, len(written))
	runs := make([]string, 0, len(written))
	for _, l := range written {
		name := sdkLanguageOf(l).name
		held = append(held, "a "+name+" manifest ("+english.And(undetected.ManifestsOf(l))+")")
		runs = append(runs, "`ocel init --lang "+name+"`")
	}
	return sdkLanguage{}, false, &clierror.Error{
		Code: clierror.CodeInitAmbiguousLanguage,
		Hint: "--lang",
		Cause: fmt.Errorf(
			"this directory holds %s, and init adds the ocel SDK for one language: run %s — %w",
			english.And(held), english.Or(runs), undetected,
		),
	}
}

func languageOfProject(projectDir string, opts initOptions) (sdkLanguage, bool, error) {
	if opts.language != "" {
		lang, err := languageNamed(opts.language)
		return lang, err == nil, err
	}
	return detectLanguage(projectDir)
}
