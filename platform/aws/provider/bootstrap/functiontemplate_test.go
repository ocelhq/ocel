package bootstrap

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func fixturePayloads() stackPayloads {
	return stackPayloads{
		optimizer:     fixtureOptimizerCode(),
		invalidator:   fixtureInvalidatorCode(),
		envSourceSync: fixtureEnvSourceSyncCode(),
	}
}

type parsedFunctionTemplate struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			MessageRetentionPeriod int    `yaml:"MessageRetentionPeriod"`
			SqsManagedSseEnabled   bool   `yaml:"SqsManagedSseEnabled"`
			Runtime                string `yaml:"Runtime"`
			Handler                string `yaml:"Handler"`
			Architectures          []string
			MemorySize             int `yaml:"MemorySize"`
			Timeout                int `yaml:"Timeout"`
			Code                   struct {
				S3Bucket string `yaml:"S3Bucket"`
				S3Key    string `yaml:"S3Key"`
			} `yaml:"Code"`
			Environment struct {
				Variables map[string]string `yaml:"Variables"`
			} `yaml:"Environment"`
			EventSourceArn                 string   `yaml:"EventSourceArn"`
			StartingPosition               string   `yaml:"StartingPosition"`
			BatchSize                      int      `yaml:"BatchSize"`
			MaximumBatchingWindowInSeconds *int     `yaml:"MaximumBatchingWindowInSeconds"`
			FunctionResponseTypes          []string `yaml:"FunctionResponseTypes"`
			MaximumRetryAttempts           *int     `yaml:"MaximumRetryAttempts"`
			DestinationConfig              struct {
				OnFailure struct {
					Destination string `yaml:"Destination"`
				} `yaml:"OnFailure"`
			} `yaml:"DestinationConfig"`
			FilterCriteria struct {
				Filters []struct {
					Pattern string `yaml:"Pattern"`
				} `yaml:"Filters"`
			} `yaml:"FilterCriteria"`
			Policies []struct {
				PolicyName     string `yaml:"PolicyName"`
				PolicyDocument struct {
					Statement []struct {
						Effect   string `yaml:"Effect"`
						Action   any    `yaml:"Action"`
						Resource any    `yaml:"Resource"`
					} `yaml:"Statement"`
				} `yaml:"PolicyDocument"`
			} `yaml:"Policies"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
}

func parseFunctionTemplate(t *testing.T, template string) parsedFunctionTemplate {
	t.Helper()
	var tmpl parsedFunctionTemplate
	if err := yaml.Unmarshal([]byte(template), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	return tmpl
}
