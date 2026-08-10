package bootstrap

import (
	"fmt"

	"github.com/ocelhq/ocel/platform/aws/provider/vars"
)

const (
	VarsTableIndexName = vars.IndexName

	outputVarsTable  = "VarsTableName"
	outputVarsKeyARN = "VarsKeyArn"
)

func varsKeyAliasFor(class string) string {
	return "alias/ocel-vars-" + class
}

func varsResources(class string) string {
	return fmt.Sprintf(`  VarsKey:
    Type: AWS::KMS::Key
    Properties:
      Description: Ocel variable store key for the %s class.
      EnableKeyRotation: true
      KeyPolicy:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Principal:
              AWS: !Sub 'arn:aws:iam::${AWS::AccountId}:root'
            Action: 'kms:*'
            Resource: '*'
  VarsKeyAlias:
    Type: AWS::KMS::Alias
    Properties:
      AliasName: %s
      TargetKeyId: !Ref VarsKey
  VarsTable:
    Type: AWS::DynamoDB::Table
    Properties:
      BillingMode: PAY_PER_REQUEST
      AttributeDefinitions:
        - AttributeName: pk
          AttributeType: S
        - AttributeName: sk
          AttributeType: S
        - AttributeName: gsi1pk
          AttributeType: S
        - AttributeName: gsi1sk
          AttributeType: S
      KeySchema:
        - AttributeName: pk
          KeyType: HASH
        - AttributeName: sk
          KeyType: RANGE
      GlobalSecondaryIndexes:
        - IndexName: %s
          KeySchema:
            - AttributeName: gsi1pk
              KeyType: HASH
            - AttributeName: gsi1sk
              KeyType: RANGE
          Projection:
            ProjectionType: KEYS_ONLY
`, class, varsKeyAliasFor(class), VarsTableIndexName)
}

func varsOutputs() string {
	return fmt.Sprintf(`  %s:
    Description: DynamoDB table holding variable values, separate from general state.
    Value: !Ref VarsTable
  %s:
    Description: KMS key every encrypted value of this class is encrypted under.
    Value: !GetAtt VarsKey.Arn
`, outputVarsTable, outputVarsKeyARN)
}
