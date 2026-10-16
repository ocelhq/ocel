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
	written, found, err := language.Detect(dir, "")
	var ambiguity *language.AmbiguityError
	if errors.As(err, &ambiguity) {
		names := make([]string, 0, len(ambiguity.Languages))
		for _, l := range ambiguity.Languages {
			names = append(names, sdkLanguageOf(l).name)
		}
		return sdkLanguage{}, false, &clierror.Error{
			Code: clierror.CodeInitAmbiguousLanguage,
			Hint: "--lang",
			Cause: fmt.Errorf(
				"this directory holds %s, so it could be a %s project: name the one this is with `--lang %s`",
				ambiguity.Holdings(), english.Or(names), names[0],
			),
		}
	}
	if err != nil {
		return sdkLanguage{}, false, &clierror.Error{
			Code:  clierror.CodeInitAmbiguousLanguage,
			Hint:  "--lang",
			Cause: fmt.Errorf("%w, so ocel cannot tell which language this project is written in: name it with `--lang`, one of %s", err, english.Or(languageNames())),
		}
	}
	if !found {
		return sdkLanguage{}, false, nil
	}
	return sdkLanguageOf(written), true, nil
}

func languageOfProject(projectDir string, opts initOptions) (sdkLanguage, bool, error) {
	if opts.language != "" {
		lang, err := languageNamed(opts.language)
		return lang, err == nil, err
	}
	return detectLanguage(projectDir)
}
