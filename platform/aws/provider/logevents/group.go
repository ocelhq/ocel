package logevents

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
)

const containerGroupPrefix = "/ocel/containers/"

type logGroup struct {
	name         string
	streamPrefix *string
	instance     func(stream string) string
	read         func(text string) (line string, failure, keep bool)
}

func findGroup(ctx context.Context, lambdas *lambda.Client, source Source, tier string) (logGroup, error) {
	if source.Function == "" {
		return logGroup{
			name:         containerGroupPrefix + tier,
			streamPrefix: aws.String(source.Container + "/"),
			instance:     taskID,
			read:         keepLine,
		}, nil
	}
	config, err := lambdas.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(source.Function)})
	if err != nil {
		return logGroup{}, fmt.Errorf("read the log group of function %s: %w", source.Function, err)
	}
	if config.LoggingConfig == nil || aws.ToString(config.LoggingConfig.LogGroup) == "" {
		return logGroup{}, fmt.Errorf("function %s names no log group", source.Function)
	}
	return logGroup{name: aws.ToString(config.LoggingConfig.LogGroup), instance: versionAndID, read: readLambdaLine}, nil
}

func keepLine(text string) (string, bool, bool) {
	return text, false, true
}

func taskID(stream string) string {
	return stream[strings.LastIndex(stream, "/")+1:]
}

func versionAndID(stream string) string {
	start := strings.Index(stream, "[")
	if start < 0 {
		return stream
	}
	return stream[start:]
}
