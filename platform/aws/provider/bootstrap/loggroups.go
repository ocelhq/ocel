package bootstrap

import "fmt"

const bootstrapLogRetentionDays = 14

func lambdaLogGroupResource(function string) string {
	return fmt.Sprintf(`  %[1]sLogGroup:
    Type: AWS::Logs::LogGroup
    Metadata:
      Description: "Where %[1]s writes its logs. Owned by this stack so its retention is bounded and deleting the stack reclaims it."
    Properties:
      LogGroupName: !Sub '/aws/lambda/${AWS::StackName}-%[1]s'
      RetentionInDays: %[2]d
`, function, bootstrapLogRetentionDays)
}

func lambdaLoggingConfig(function string) string {
	return fmt.Sprintf(`      LoggingConfig:
        LogGroup: !Ref %sLogGroup
`, function)
}
