package naming

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	AppWord        = "app"
	productionWord = "prod"
)

func AppFence(namespace string) string {
	return NamespaceField(namespace) + WordSeparator + AppWord + WordSeparator
}

func TierWord(tier environment.Tier) string {
	if tier == environment.TierProduction {
		return productionWord
	}
	return Sanitize(string(tier))
}

const minCutFieldChars = 3

type PhysicalName struct {
	Namespace string
	Project   string
	Env       string
	App       string
	Declared  string
	Tag       string
}

func (c Coordinate) AppResourceName(namespace, tag string) PhysicalName {
	return PhysicalName{Namespace: namespace, Project: c.Project, Env: c.Env, App: c.appField(), Declared: c.Name, Tag: tag}
}

func (p PhysicalName) Fit(limit int) (string, error) {
	return p.fit(limit, 0)
}

func (p PhysicalName) FitWithChildren(limit int, childSuffixes ...string) (string, error) {
	reserved := 0
	for _, suffix := range childSuffixes {
		reserved = max(reserved, len(WordSeparator)+len(Sanitize(suffix)))
	}
	return p.fit(limit, reserved)
}

func (p PhysicalName) fit(limit, reserved int) (string, error) {
	for field, value := range map[string]string{"namespace": p.Namespace, "project": p.Project, "env": p.Env, "declared name": p.Declared, "tag": p.Tag} {
		if value == "" {
			return "", fmt.Errorf("a physical name needs a %s", field)
		}
	}
	project, env, app := Sanitize(p.Project), Sanitize(p.Env), ""
	if p.App != "" && p.App != InfraApp {
		app = Sanitize(p.App)
	}
	name := func() string {
		return AppFence(p.Namespace) + Join(WordSeparator, project, env, app, p.Declared, p.Tag)
	}
	full := name()
	for _, field := range []*string{&env, &project, &app} {
		over := len(name()) + reserved - limit
		if over <= 0 {
			break
		}
		keep := max(len(*field)-over, min(len(*field), minCutFieldChars))
		*field = strings.TrimRight((*field)[:keep], WordSeparator)
	}
	fitted := name()
	if len(fitted)+reserved <= limit {
		return fitted, nil
	}
	if reserved > 0 {
		return "", fmt.Errorf("%q does not fit %d characters with %d kept for its children's suffixes, even with env, project and app cut to %d characters each", full, limit, reserved, minCutFieldChars)
	}
	return "", fmt.Errorf("%q does not fit %d characters, even with env, project and app cut to %d characters each", full, limit, minCutFieldChars)
}

func OcelName(namespace string, words ...string) (string, error) {
	rest := Join(WordSeparator, words...)
	if rest == "" {
		return "", fmt.Errorf("an Ocel-owned name in namespace %q needs a word after the namespace", namespace)
	}
	if first, _, _ := strings.Cut(rest, WordSeparator); first == AppWord {
		return "", fmt.Errorf("the Ocel-owned name %q starts with the fence %q that only app resources are named under", NamespaceField(namespace)+WordSeparator+rest, AppFence(namespace))
	}
	return NamespaceField(namespace) + WordSeparator + rest, nil
}
