package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var version = "dev"

var apiURLFlag string

var verboseFlag bool

var configFlag string

func explicitConfigPath() string {
	if configFlag != "" {
		return configFlag
	}
	return os.Getenv("OCEL_CONFIG")
}

func verboseEnabled() bool {
	if verboseFlag {
		return true
	}
	_, ok := os.LookupEnv("OCEL_DEBUG")
	return ok
}

const (
	logFormatHuman = "human"
	logFormatJSON  = "json"
)

var logFormatFlag string

func logFormat() string {
	if logFormatFlag == logFormatJSON {
		return logFormatJSON
	}
	return logFormatHuman
}

var rootCmd = &cobra.Command{
	Use:           "ocel",
	Short:         "Ocel CLI",
	Long:          "Ocel CLI\n\nocel deploys apps to your own cloud",
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&apiURLFlag, "api-url", "", "Base URL of the Ocel server (defaults to $OCEL_API_URL, else https://ocel.app)")
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "Stream full deploy logs to the terminal instead of the phased progress view (also OCEL_DEBUG)")
	rootCmd.PersistentFlags().StringVarP(&configFlag, "config", "c", "", "Path to the project config file, resolved relative to the working directory (defaults to $OCEL_CONFIG, else the nearest ocel.config.ts)")
	rootCmd.PersistentFlags().StringVar(&logFormatFlag, "log-format", logFormatHuman, "Output format for command logs: human or json")

	rootCmd.AddCommand(devCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(generateCmd)
	rootCmd.AddCommand(buildCmd)
	rootCmd.AddCommand(deployCmd)
	rootCmd.AddCommand(previewCmd)
	rootCmd.AddCommand(rollbackCmd)
	rootCmd.AddCommand(deploymentsCmd)
	rootCmd.AddCommand(bootstrapCmd)
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
}

func effectiveAPIURL(cmd *cobra.Command, credsURL string) string {
	if cmd != nil && cmd.Flags().Changed("api-url") {
		return strings.TrimRight(apiURLFlag, "/")
	}
	if credsURL != "" {
		return credsURL
	}
	return resolveAPIURL()
}
