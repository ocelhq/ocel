package docker

const (
	LogDriver   = "local"
	LogMaxSize  = "20m"
	LogMaxFiles = "5"
	LogCeiling  = 20 * 5 << 20
)

const (
	PidsLimit       = "4096"
	NoNewPrivileges = "no-new-privileges"
)

func Logging() []string {
	return []string{"--log-driver", LogDriver, "--log-opt", "max-size=" + LogMaxSize, "--log-opt", "max-file=" + LogMaxFiles}
}

func Confined(capabilities []string, fileCapabilities bool) []string {
	argv := []string{"--pids-limit", PidsLimit}
	if !fileCapabilities {
		argv = append(argv, "--security-opt", NoNewPrivileges)
	}
	argv = append(argv, "--cap-drop", "ALL")
	for _, capability := range capabilities {
		argv = append(argv, "--cap-add", capability)
	}
	return argv
}
