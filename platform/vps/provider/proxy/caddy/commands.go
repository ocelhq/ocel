package caddy

import "strings"

const (
	logTail           = "200"
	wildcardDirectory = "wildcard_"
	hostnameMax       = 253
)

var certificateStore = DataMount + "/caddy/certificates"

func reloading() string {
	return inside("caddy", "reload", "--config", ConfigMount, "--address", "unix/"+AdminSocket)
}

func listening() string {
	return inside("sh", "-c", "cat /proc/net/tcp && { cat /proc/net/tcp6 2>/dev/null || :; }")
}

func logging() string {
	return "docker logs --timestamps --tail " + logTail + " " + quoted(Container) + " 2>&1 || true"
}

func forgetting(hostnames []string) string {
	script := "for issuer in " + quoted(certificateStore) + "/*/; do\n" +
		"[ -d \"$issuer\" ] || continue\n" +
		"for name in " + words(hostnames) + "; do\n" +
		"held=\"${issuer}${name}\"\n" +
		"if [ -e \"$held\" ]; then rm -rf -- \"$held\" && printf '%s\\n' \"$held\"; fi\n" +
		"done\n" +
		"done"
	return inside("sh", "-c", script)
}

func subject(hostname string) bool {
	if hostname == "" || len(hostname) > hostnameMax {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || strings.HasPrefix(label, wildcardDirectory) {
			return false
		}
		for _, r := range label {
			letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
			if !letter && (r < '0' || r > '9') && r != '-' && r != '_' {
				return false
			}
		}
	}
	return true
}
