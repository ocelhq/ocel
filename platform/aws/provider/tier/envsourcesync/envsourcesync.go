package envsourcesync

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	Part    = "env-source-sync"
	Summary = "the function that reads each scheduled env source of the tier once a minute and writes what changed"

	runtime         = "provided.al2023"
	architecture    = "arm64"
	handler         = "bootstrap"
	memoryMB        = 128
	timeoutSeconds  = 60
	rate            = "rate(1 minute)"
	maxEventSeconds = 60
	retentionDays   = 14

	lambdaPrincipal    = "lambda.amazonaws.com"
	schedulerPrincipal = "scheduler.amazonaws.com"
	syncPolicyName     = "env-source-sync"
	invokePolicyName   = "invoke-env-source-sync"

	syncWord     = "env-sync"
	scheduleWord = "schedule"
)

type Spec struct {
	Namespace string
	Tier      environment.Tier
	Region    string
	Boundary  string
	RolePath  string
	Table     string
	TableARN  string
	Key       string

	UpdatesTrust bool
}

type Names struct {
	Function      string
	Role          string
	ScheduleRole  string
	LogGroup      string
	ScheduleGroup string
	Schedule      string
}

func NamesFor(namespace string, tier environment.Tier) Names {
	function := naming.OcelName(namespace, tier, syncWord)
	return Names{
		Function:      function,
		Role:          function,
		ScheduleRole:  naming.OcelName(namespace, tier, syncWord, scheduleWord),
		LogGroup:      "/aws/lambda/" + function,
		ScheduleGroup: function,
		Schedule:      function,
	}
}

func Digest(spec Spec) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		payloads.EnvSourceSyncSHA256(), spec.Namespace, string(spec.Tier), spec.Region, spec.Boundary, spec.RolePath, spec.Table, spec.TableARN, spec.Key,
	}, "\n")))
	return hex.EncodeToString(sum[:])
}

func Inventory(namespace string, tier environment.Tier) []pricing.Resource {
	names := NamesFor(namespace, tier)
	return []pricing.Resource{
		{Name: names.Function, Type: "aws_lambda_function", Properties: map[string]any{
			"runtime": runtime, "memory_size": memoryMB, "timeout": timeoutSeconds, "architectures": []any{architecture},
		}},
		{Name: names.LogGroup, Type: "aws_cloudwatch_log_group", Properties: map[string]any{"retention_in_days": retentionDays}},
	}
}
