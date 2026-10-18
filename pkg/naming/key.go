package naming

const (
	tokenProject = "PROJECT"
	tokenStack   = "STACK"
	tokenTag     = "TAG"
	tokenEnv     = "ENV"
	tokenSession = "SESSION"
	tokenTasks   = "TASKS"
)

func SessionKeyPrefix(project, env string) string {
	return token(tokenProject, project) + KeySeparator + token(tokenEnv, env) + KeySeparator + tokenSession + KeySeparator
}

func TaskKeyPrefix(project, env string) string {
	return token(tokenProject, project) + KeySeparator + token(tokenEnv, env) + KeySeparator + tokenTasks + KeySeparator
}

func StackKey(project string, stack StackName) string {
	return token(tokenProject, project) + KeySeparator + token(tokenStack, stack.String())
}

func ISRTagPrefix(project string, stack StackName) string {
	return StackKey(project, stack) + KeySeparator + tokenTag + KeySeparator
}

func token(name, value string) string {
	return name + KeySeparator + value
}
