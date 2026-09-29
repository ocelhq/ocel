package variables

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/runui"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func RefuseImpliedInFolder(scope Scope, key, folder string) error {
	if folder == "" {
		return nil
	}
	if readers := scope.readers(key); len(readers) > 0 {
		return fmt.Errorf("%s is read by %s, and a binding serves the whole project, so it reads the value at the project root and a value in %s would reach nothing: set it without --folder", key, sites(readers), folder)
	}
	if scope.EnvSource.isCredential(key) {
		return fmt.Errorf("%s is what ocel logs in to %s with, and ocel reads it at the project root alone, so a value in %s would reach nothing: set it without --folder", key, scope.EnvSource.ID, folder)
	}
	return nil
}

func RefuseUnwritable(definitions []*resourcesv1.VariableDefinition, key, folder string) error {
	for _, definition := range definitions {
		if definition.GetKey() != key {
			continue
		}
		scope := definition.GetFolders()
		if len(scope) == 0 || slices.Contains(scope, folder) {
			return nil
		}
		if folder == "" {
			return fmt.Errorf("%s is scoped to %s, so it has no value at the project root — nothing would read one. Set it with --folder %s instead%s",
				key, strings.Join(scope, " and "), scope[0], runui.VariableDescriptionLine(definition.GetDescription()))
		}
		return fmt.Errorf("%s is scoped to %s, so %s has no value for it. Set it in one of the folders it names, or widen the scope where it is declared%s",
			key, strings.Join(scope, " and "), folder, runui.VariableDescriptionLine(definition.GetDescription()))
	}
	return fmt.Errorf("no app in this project declares %s, so a value stored under it would be delivered to nothing: "+
		"declare it in a defineEnv call — `defineEnv({ %s: { class: \"plain\" } })` — and set it again",
		key, key)
}
