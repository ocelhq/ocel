package boxapi

const (
	RunnerConfigPath = TierRoot + "/runner.json"
	RunnerDir        = StateRoot + "/runner"
	RunnerDocker     = LibDir + "/runner-docker"
	RunnerCache      = "ocel-runner-cache"
	RunnerImage      = "ocel-job"
	RunnerSlice      = "ocel-jobs.slice"

	RunnerJobsDaemon       = "ocel-jobs-docker"
	RunnerJobsDaemonDir    = StateRoot + "/jobs-docker"
	RunnerJobsDaemonSocket = RunnerJobsDaemonDir + "/docker.sock"
)

type RunnerConfig struct {
	URL   string `json:"url"`
	Image string `json:"image"`
}

func RunnerDockerArgv(args ...string) []string {
	return append([]string{"sudo", "-n", RunnerDocker}, args...)
}
