package bootstrap

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	envSyncKeyPrefix = "ocel-envsync"

	envSyncLabel = "env syncer"

	envSyncRuntime      = "provided.al2023"
	envSyncArchitecture = "arm64"
	envSyncHandler      = "bootstrap"
	envSyncMemoryMB     = 128

	envSyncTimeoutSeconds = 60

	envSyncRate = "rate(1 minute)"

	envSyncScheduleGroup = "default"

	envSyncVarsTableEnvVar = "OCEL_VARS_TABLE"
	envSyncVarsKeyEnvVar   = "OCEL_VARS_KEY"
	envSyncClassEnvVar     = "OCEL_INFRA_CLASS"

	schedulerServicePrincipal = "scheduler.amazonaws.com"
)

func envSyncPlacement(bucket string) payloads.Placement {
	return payloads.At(bucket, envSyncKeyPrefix, payloads.EnvSync())
}

func ensureEnvSyncPayload(ctx context.Context, store ObjectStore, bucket string) (payloads.Placement, error) {
	return payloads.Place(ctx, store, bucket, envSyncKeyPrefix, envSyncLabel, payloads.EnvSync())
}

func envSyncResources(ns Namespace, code payloads.Placement, class, key string) string {
	return fmt.Sprintf(`  EnvSyncRole:
    Type: AWS::IAM::Role
    Properties:
      Description: "Execution role for the %[1]s env syncer: items in the %[1]s vars table, sealing and opening under its key, and its own log group."
      AssumeRolePolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Principal:
              Service: %[2]s
            Action: sts:AssumeRole
      Policies:
        - PolicyName: %[3]s
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action:
                  - dynamodb:DeleteItem
                  - dynamodb:GetItem
                  - dynamodb:PutItem
                  - dynamodb:Query
                Resource: !Ref %[4]s
              - Effect: Allow
                Action:
                  - kms:Decrypt
                  - kms:Encrypt
                Resource: %[5]s
              - Effect: Allow
                Action:
                  - logs:CreateLogStream
                  - logs:PutLogEvents
                Resource: !GetAtt EnvSyncLogGroup.Arn
  EnvSync:
    Type: AWS::Lambda::Function
    Properties:
      Description: "Ocel env syncer - polls each standing env source registered in the %[1]s class and writes what changed into its vars table."
      Runtime: %[6]s
      Architectures:
        - %[7]s
      Handler: %[8]s
      MemorySize: %[9]d
      Timeout: %[10]d
      Role: !GetAtt EnvSyncRole.Arn
`+lambdaLoggingConfig("EnvSync")+`      Code:
        S3Bucket: %[11]s
        S3Key: %[12]s
      Environment:
        Variables:
          %[13]s: !Ref %[14]s
          %[15]s: %[5]s
          %[16]s: '%[1]s'
`+lambdaLogGroupResource("EnvSync")+`  EnvSyncScheduleRole:
    Type: AWS::IAM::Role
    Properties:
      Description: "Role EventBridge Scheduler invokes the %[1]s env syncer through, and nothing else."
      AssumeRolePolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Principal:
              Service: %[17]s
            Action: sts:AssumeRole
            Condition:
              StringEquals:
                aws:SourceAccount: !Sub '${AWS::AccountId}'
              ArnLike:
                aws:SourceArn: !Sub 'arn:${AWS::Partition}:scheduler:${AWS::Region}:${AWS::AccountId}:schedule-group/%[18]s'
      Policies:
        - PolicyName: %[19]s
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action: lambda:InvokeFunction
                Resource: !GetAtt EnvSync.Arn
  EnvSyncSchedule:
    Type: AWS::Scheduler::Schedule
    Properties:
      Description: "Runs the %[1]s env syncer once a minute. Each source waits out its own backoff, so a failing one is not polled every minute."
      Name: %[20]s
      ScheduleExpression: %[21]s
      FlexibleTimeWindow:
        Mode: 'OFF'
      State: ENABLED
      Target:
        Arn: !GetAtt EnvSync.Arn
        RoleArn: !GetAtt EnvSyncScheduleRole.Arn
        RetryPolicy:
          MaximumRetryAttempts: 0
`, class, LambdaServicePrincipal, ns.PolicyName("envsync"), paramVarsTableARN, key,
		envSyncRuntime, envSyncArchitecture, envSyncHandler, envSyncMemoryMB, envSyncTimeoutSeconds,
		code.Bucket, code.Key,
		envSyncVarsTableEnvVar, paramVarsTableName, envSyncVarsKeyEnvVar, envSyncClassEnvVar,
		schedulerServicePrincipal, envSyncScheduleGroup, ns.PolicyName("envsync-schedule"),
		ns.envSyncScheduleName(class), envSyncRate)
}
