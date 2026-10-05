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
	CodePrerequisiteMissing        = "prerequisite.missing"
	CodeVariablesMissing           = "variables.missing"
	CodeBootstrapMissing           = "bootstrap.missing"
	CodeBootstrapFeaturesMissing   = "bootstrap.features_missing"
	CodeConsoleNotLoggedIn         = "console.not_logged_in"
	CodeConsoleNotLinked           = "console.not_linked"
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
		CodePrerequisiteMissing,
		CodeVariablesMissing,
		CodeBootstrapMissing,
		CodeBootstrapFeaturesMissing,
		CodeConsoleNotLoggedIn,
		CodeConsoleNotLinked,
		CodeDoctorChecksFailed,
		CodeDevCommandFailed,
	}
}
