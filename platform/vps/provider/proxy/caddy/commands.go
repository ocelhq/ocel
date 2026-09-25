package caddy

import "strings"

const (
	logTail           = "200"
	wildcardDirectory = "wildcard_"
	hostnameMax       = 253
)

var certificateStore = DataMount + "/caddy/certificates"

const forgettingScript = `store=$1
shift
for issuer in "$store"/*/; do
[ -d "$issuer" ] || continue
for name in "$@"; do
held="${issuer}${name}"
if [ -e "$held" ]; then rm -rf -- "$held" && printf '%s\n' "$held"; fi
done
done`

func reloading() []string {
	return inside("caddy", "reload", "--config", ConfigMount, "--address", "unix/"+AdminSocket)
}

func listening() []string {
	return inside("sh", "-c", "cat /proc/net/tcp && { cat /proc/net/tcp6 2>/dev/null || :; }")
}

func logging() []string {
	return []string{"docker", "logs", "--timestamps", "--tail", logTail, Container}
}

func forgetting(hostnames []string) []string {
	return inside(append([]string{"sh", "-c", forgettingScript, "sh", certificateStore}, hostnames...)...)
}

func certifiable(hostname string) bool {
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
