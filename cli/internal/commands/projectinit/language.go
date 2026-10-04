package projectinit

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/language"
)

type sdkLanguage struct {
	name     string
	language language.Language
	add      []string
}

var sdkLanguages = []sdkLanguage{
	{name: "go", language: language.Go, add: []string{"go", "get", goSDKModule}},
	{name: "rust", language: language.Rust, add: []string{"cargo", "add", rustSDKCrate}},
	{name: "python", language: language.Python, add: []string{"uv", "add", sdkPackage}},
	{name: "node", language: language.JS},
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
	found := language.Manifested(dir)
	switch len(found) {
	case 0:
		return sdkLanguage{}, false, nil
	case 1:
		return sdkLanguageOf(found[0]), true, nil
	default:
		names := make([]string, 0, len(found))
		for _, l := range found {
			names = append(names, sdkLanguageOf(l).name)
		}
		return sdkLanguage{}, false, &clierror.Error{
			Code: codeAmbiguousLanguage,
			Hint: "--lang",
			Cause: fmt.Errorf(
				"this directory contains the manifests of %s at once, so it could be a %s project: name the one this is with `--lang %s`",
				strings.Join(names, " and "), strings.Join(names, " or "), names[0],
			),
		}
	}
}

func languageOfProject(projectDir string, opts initOptions) (sdkLanguage, bool, error) {
	if opts.language != "" {
		lang, err := languageNamed(opts.language)
		return lang, err == nil, err
	}
	return detectLanguage(projectDir)
}
