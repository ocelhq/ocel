package logevents

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/ocelhq/ocel/pkg/environment"
)

type logGroup struct {
	name          string
	streamPrefix  *string
	parseInstance func(stream string) string
	parseLine     func(message string) (event Event, keep bool)
}

func NameContainerGroup(tier environment.Tier) string {
	return "/ocel/containers/" + string(tier)
}

func findGroup(ctx context.Context, lambdas *lambda.Client, source Source, tier environment.Tier) (logGroup, error) {
	if source.Function == "" {
		return logGroup{
			name:          NameContainerGroup(tier),
			streamPrefix:  aws.String(source.Container + "/"),
			parseInstance: parseTaskID,
			parseLine:     keepLine,
		}, nil
	}
	config, err := lambdas.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(source.Function)})
	if err != nil {
		return logGroup{}, fmt.Errorf("read the log group of function %s: %w", source.Function, err)
	}
	if config.LoggingConfig == nil || aws.ToString(config.LoggingConfig.LogGroup) == "" {
		return logGroup{}, fmt.Errorf("function %s names no log group", source.Function)
	}
	return logGroup{name: aws.ToString(config.LoggingConfig.LogGroup), parseInstance: parseVersionAndID, parseLine: parseLambdaLine}, nil
}

func keepLine(message string) (Event, bool) {
	return Event{Text: message}, true
}

func parseTaskID(stream string) string {
	return stream[strings.LastIndex(stream, "/")+1:]
}

func parseVersionAndID(stream string) string {
	start := strings.Index(stream, "[")
	if start < 0 {
		return stream
	}
	return stream[start:]
}
