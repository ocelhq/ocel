package caddy

const logTail = "200"

func reloading() []string {
	return inside("caddy", "reload", "--config", ConfigMount, "--address", "unix/"+AdminSocket)
}

func listening() []string {
	return inside("sh", "-c", "cat /proc/net/tcp && { cat /proc/net/tcp6 2>/dev/null || :; }")
}

func logging() []string {
	return []string{"docker", "logs", "--timestamps", "--tail", logTail, Container}
}
