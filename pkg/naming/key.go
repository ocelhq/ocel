package naming

import (
	"fmt"
	"strings"
)

const (
	tokenProject = "PROJECT"
	tokenStack   = "STACK"
	tokenClass   = "CLASS"
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

func ISRTagPrefix(project string, stack StackName) string {
	return StackKey(project, stack) + KeySeparator + tokenTag + KeySeparator
}

func ISRTagKey(project string, stack StackName, tag string) string {
	return ISRTagPrefix(project, stack) + tag
}

func ParseStackKey(key string) (string, StackName, error) {
	project, err := ProjectOf(key)
	if err != nil {
		return "", StackName{}, err
	}
	rest, ok := strings.CutPrefix(key, token(tokenProject, project)+KeySeparator+token(tokenStack, ""))
	if !ok {
		return "", StackName{}, fmt.Errorf("key %q names no stack", key)
	}
	stack, err := ParseStackName(rest)
	if err != nil {
		return "", StackName{}, fmt.Errorf("key %q: %w", key, err)
	}
	return project, stack, nil
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
