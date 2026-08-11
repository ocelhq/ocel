package naming

import (
	"fmt"
	"strings"
)

const (
	tokenProject = "PROJECT"
	tokenStack   = "STACK"
	tokenClass   = "CLASS"
	tokenApp     = "APP"
	tokenTag     = "TAG"
)

func ProjectKey(project string) string {
	return token(tokenProject, project)
}

func StackKey(project string, stack StackName) string {
	return token(tokenProject, project) + KeySeparator + token(tokenStack, stack.String())
}

func VarsKey(project, class string) string {
	return token(tokenProject, project) + KeySeparator + token(tokenClass, class)
}

func ISRTagKey(project, app, tag string) string {
	return token(tokenProject, project) + KeySeparator + token(tokenApp, app) + KeySeparator + token(tokenTag, tag)
}

func ProjectOf(key string) (string, error) {
	fields := strings.Split(key, KeySeparator)
	if len(fields) < 2 || fields[0] != tokenProject || fields[1] == "" {
		return "", fmt.Errorf("key %q carries no project scope", key)
	}
	return fields[1], nil
}

func token(name, value string) string {
	return name + KeySeparator + value
}
