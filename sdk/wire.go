package ocel

const (
	phaseEnv          = "OCEL_PHASE"
	devServerEnv      = "OCEL_DEV_SERVER"
	devServerTokenEnv = "OCEL_DEV_SERVER_TOKEN"
	appFolderEnv      = "OCEL_APP_FOLDER"
	appURLEnv         = "OCEL_URL"
	runtimeAddressEnv = "OCEL_RUNTIME_ADDRESS"
	liveDirEnv        = "OCEL_LIVE_DIR"
	sessionTokenEnv   = "OCEL_SESSION_TOKEN"
)

func bearer(token string) string {
	return "Bearer " + token
}
