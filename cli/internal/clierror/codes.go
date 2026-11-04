package clierror

const internalCode = "internal"

const (
	CodeConfirmationRequired       = "confirmation_required"
	CodeConfirmationBypassMismatch = "confirmation_bypass_mismatch"
	CodeInputRequired              = "input_required"
	CodeUsage                      = "usage"
	CodeInterrupted                = "interrupted"
	CodeProjectNoConfig            = "project.no_config"
	CodeProjectInvalidConfig       = "project.invalid_config"
	CodeSDKVersionMismatch         = "sdk.version_mismatch"
	CodeInitConfigExists           = "init.config_exists"
	CodeInitAmbiguousLanguage      = "init.ambiguous_language"
	CodeProviderUnavailable        = "provider.unavailable"
	CodeProviderVersionMismatch    = "provider.version_mismatch"
	CodeProviderUnsupported        = "provider.unsupported"
	CodeProviderDenied             = "provider.denied"
	CodeProviderNotReady           = "provider.not_ready"
	CodeProviderNotFound           = "provider.not_found"
	CodeProviderInvalidRequest     = "provider.invalid_request"
	CodeProviderBusy               = "provider.busy"
	CodePrerequisiteMissing        = "prerequisite.missing"
	CodeVariablesMissing           = "variables.missing"
	CodeBootstrapMissing           = "bootstrap.missing"
	CodeBootstrapPartsMissing      = "bootstrap.parts_missing"
	CodeConsoleNotLoggedIn         = "console.not_logged_in"
	CodeConsoleNotLinked           = "console.not_linked"
	CodeCloudUnsupported           = "cloud.unsupported"
	CodeCloudVersionUnserved       = "cloud.version_unserved"
	CodeCloudTokenRefused          = "cloud.token_refused"
	CodeJobUnsupported             = "job.unsupported"
	CodeJobFailed                  = "job.failed"
	CodeJobCanceled                = "job.canceled"
	CodeJobSourceInvalid           = "job.source_invalid"
	CodeJobContextInvalid          = "job.context_invalid"
	CodeJobVersionUnserved         = "job.version_unserved"
	CodeJobTokenRefused            = "job.token_refused"
	CodeDoctorChecksFailed         = "doctor.checks_failed"
	CodeDevCommandFailed           = "dev.command_failed"
)

func Codes() []string {
	return []string{
		CodeConfirmationRequired,
		CodeConfirmationBypassMismatch,
		CodeInputRequired,
		CodeUsage,
		CodeInterrupted,
		CodeProjectNoConfig,
		CodeProjectInvalidConfig,
		CodeSDKVersionMismatch,
		CodeInitConfigExists,
		CodeInitAmbiguousLanguage,
		CodeProviderUnavailable,
		CodeProviderVersionMismatch,
		CodeProviderUnsupported,
		CodeProviderDenied,
		CodeProviderNotReady,
		CodeProviderNotFound,
		CodeProviderInvalidRequest,
		CodeProviderBusy,
		CodePrerequisiteMissing,
		CodeVariablesMissing,
		CodeBootstrapMissing,
		CodeBootstrapPartsMissing,
		CodeConsoleNotLoggedIn,
		CodeConsoleNotLinked,
		CodeCloudUnsupported,
		CodeCloudVersionUnserved,
		CodeCloudTokenRefused,
		CodeJobUnsupported,
		CodeJobFailed,
		CodeJobCanceled,
		CodeJobSourceInvalid,
		CodeJobContextInvalid,
		CodeJobVersionUnserved,
		CodeJobTokenRefused,
		CodeDoctorChecksFailed,
		CodeDevCommandFailed,
	}
}

func CloudCodes() []string {
	return []string{
		"cloud.preview_limit",
	}
}
