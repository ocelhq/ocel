package caddy

const logTail = "200"

func ReloadCommand() []string {
	return []string{"caddy", "reload", "--config", ConfigMount, "--address", "unix/" + AdminSocket}
}

func ListenersCommand() []string {
	return []string{"sh", "-c", "cat /proc/net/tcp && { cat /proc/net/tcp6 2>/dev/null || :; }"}
}

func reloading() []string { return inside(ReloadCommand()...) }

func listening() []string { return inside(ListenersCommand()...) }

func logging() []string {
	return []string{"docker", "logs", "--timestamps", "--tail", logTail, Container}
}
