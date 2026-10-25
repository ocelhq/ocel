package variables

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/variables/wording"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func RefuseImpliedInFolder(scope Scope, key, folder string) error {
	if folder == "" {
		return nil
	}
	if readers := scope.readers(key); len(readers) > 0 {
		return fmt.Errorf("%s is read by %s at the project root, so a value in %s reaches nothing: set it without --folder", key, sites(readers), folder)
	}
	if scope.EnvSource.isCredential(key) {
		return fmt.Errorf("%s is ocel's login to %s, read at the project root, so a value in %s reaches nothing: set it without --folder", key, scope.EnvSource.ID, folder)
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
			return fmt.Errorf("%s is scoped to %s: set it with --folder %s%s",
				key, strings.Join(scope, " and "), scope[0], wording.DescriptionLine(definition.GetDescription()))
		}
		return fmt.Errorf("%s is scoped to %s, not %s: set it in one of those, or widen its scope%s",
			key, strings.Join(scope, " and "), folder, wording.DescriptionLine(definition.GetDescription()))
	}
	return fmt.Errorf("no app in this project declares %s: "+
		"declare it with `defineEnv({ %s: { class: \"plain\" } })`, then set it again",
		key, key)
}
