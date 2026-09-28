package bootstrap

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	envSourceSyncKeyPrefix = "ocel-envsourcesync"

	envSourceSyncLabel = "env source sync"

	envSourceSyncRuntime      = "provided.al2023"
	envSourceSyncArchitecture = "arm64"
	envSourceSyncHandler      = "bootstrap"
	envSourceSyncMemoryMB     = 128

	envSourceSyncTimeoutSeconds = 60

	envSourceSyncRate = "rate(1 minute)"

	schedulerServicePrincipal = "scheduler.amazonaws.com"
)

func envSourceSyncPlacement(bucket string) payloads.Placement {
	return payloads.At(bucket, envSourceSyncKeyPrefix, payloads.EnvSourceSync())
}

func ensureEnvSourceSyncPayload(ctx context.Context, store ObjectStore, bucket string) (payloads.Placement, error) {
	return payloads.Place(ctx, store, bucket, envSourceSyncKeyPrefix, envSourceSyncLabel, payloads.EnvSourceSync())
}

func envSourceSyncResources(ns Namespace, code payloads.Placement, tier environment.Tier, key string) string {
	return fmt.Sprintf(`  EnvSourceSyncRole:
    Type: AWS::IAM::Role
    Properties:
      Description: "Execution role for the %[1]s env source sync: items in the %[1]s vars table, encrypting and decrypting under its key, and its own log group."
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
                Resource: !GetAtt EnvSourceSyncLogGroup.Arn
  EnvSourceSync:
    Type: AWS::Lambda::Function
    Properties:
      Description: "Ocel env source sync - reads each scheduled env source registered in the %[1]s tier and writes what changed into its vars table."
      Runtime: %[6]s
      Architectures:
        - %[7]s
      Handler: %[8]s
      MemorySize: %[9]d
      Timeout: %[10]d
      Role: !GetAtt EnvSourceSyncRole.Arn
`+lambdaLoggingConfig("EnvSourceSync")+`      Code:
        S3Bucket: %[11]s
        S3Key: %[12]s
      Environment:
        Variables:
          %[13]s: !Ref %[14]s
          %[15]s: %[5]s
          %[16]s: '%[1]s'
`+lambdaLogGroupResource("EnvSourceSync")+`  EnvSourceSyncInvokeConfig:
    Type: AWS::Lambda::EventInvokeConfig
    Properties:
      FunctionName: !Ref EnvSourceSync
      Qualifier: $LATEST
      MaximumRetryAttempts: 0
      MaximumEventAgeInSeconds: 60
  EnvSourceSyncScheduleGroup:
    Type: AWS::Scheduler::ScheduleGroup
    Metadata:
      Description: "The %[1]s tier's schedule group. A schedule takes no tags, so the bootstrap credential reaches this group's schedules by the group's name."
    Properties:
      Name: %[17]s
  EnvSourceSyncScheduleRole:
    Type: AWS::IAM::Role
    Properties:
      Description: "Role EventBridge Scheduler invokes the %[1]s env source sync through, and nothing else."
      AssumeRolePolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Principal:
              Service: %[18]s
            Action: sts:AssumeRole
            Condition:
              StringEquals:
                aws:SourceAccount: !Sub '${AWS::AccountId}'
              ArnEquals:
                aws:SourceArn: !GetAtt EnvSourceSyncScheduleGroup.Arn
      Policies:
        - PolicyName: %[19]s
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action: lambda:InvokeFunction
                Resource: !GetAtt EnvSourceSync.Arn
  EnvSourceSyncSchedule:
    Type: AWS::Scheduler::Schedule
    Properties:
      Description: "Runs the %[1]s env source sync once a minute. Each env source waits out its own backoff, so a failing one is not read every minute."
      Name: %[20]s
      GroupName: !Ref EnvSourceSyncScheduleGroup
      ScheduleExpression: %[21]s
      FlexibleTimeWindow:
        Mode: 'OFF'
      State: ENABLED
      Target:
        Arn: !GetAtt EnvSourceSync.Arn
        RoleArn: !GetAtt EnvSourceSyncScheduleRole.Arn
        RetryPolicy:
          MaximumRetryAttempts: 0
`, tier, LambdaServicePrincipal, ns.PolicyName("envsourcesync"), paramVarsTableARN, key,
		envSourceSyncRuntime, envSourceSyncArchitecture, envSourceSyncHandler, envSourceSyncMemoryMB, envSourceSyncTimeoutSeconds,
		code.Bucket, code.Key,
		awsports.VarsTableEnvVar, paramVarsTableName, awsports.VarsKeyEnvVar, awsports.TierEnvVar,
		ns.envSourceSyncScheduleGroupName(tier), schedulerServicePrincipal, ns.PolicyName("envsourcesync-schedule"),
		ns.envSourceSyncScheduleName(tier), envSourceSyncRate)
}
