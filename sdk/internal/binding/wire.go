package binding

const (
	PhaseEnv          = "OCEL_PHASE"
	DevServerEnv      = "OCEL_DEV_SERVER"
	DevServerTokenEnv = "OCEL_DEV_SERVER_TOKEN"
	AppFolderEnv      = "OCEL_APP_FOLDER"
	AppURLEnv         = "OCEL_URL"
	RuntimeAddressEnv = "OCEL_RUNTIME_ADDRESS"
	LiveDirEnv        = "OCEL_LIVE_DIR"
	SessionTokenEnv   = "OCEL_SESSION_TOKEN"
	sdkVersionHeader  = "Ocel-Sdk-Version"
)

func Bearer(token string) string {
	return "Bearer " + token
}
