package telemetry

var allowedEvents = []string{"command_completed", "dev_session_ended", "init_completed"}

var allowedProperties = []string{
	"command",
	"flags",
	"exit_code",
	"error_code",
	"duration_ms",
	"json",
	"tty",
	"reloads",
	"resource_kinds",
	"error_codes",
	"language",
	"package_manager",
	"provider",
	"config_format",
	"cli_version",
	"os",
	"arch",
	"agent",
	"ci",
	"$process_person_profile",
}
