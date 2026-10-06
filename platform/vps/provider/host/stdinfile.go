package host

import "path/filepath"

func writtenFromStdin(path string, parents bool) string {
	command := "umask 077 && cat >" + quoted(path) + " && chmod 0600 " + quoted(path)
	if parents {
		return "mkdir -p " + quoted(filepath.Dir(path)) + " && " + command
	}
	return command
}
