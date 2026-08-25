package bootstrap

import (
	"fmt"

	"github.com/ocelhq/ocel/platform/aws/provider/tagclock"
)

const (
	VarsTableIndexName = tagclock.IndexName

	outputVarsTable  = "VarsTableName"
	outputVarsKeyARN = "VarsKeyArn"

	varsKeyComponentTagKey   = "ocel:component"
	varsKeyComponentTagValue = "vars-key"
)

func varsKeyAliasFor(class string) string {
	return "alias/ocel-vars-" + class
}

func varsResources(class string) string {
	return fmt.Sprintf(`  VarsKey:
    Type: AWS::KMS::Key
    Properties:
      Description: "Ocel: the key every encrypted variable of the %s class is encrypted under, written when a value is set and read by deploys and running functions. Scheduling its deletion strands those values - each has to be set again under a new key."
      EnableKeyRotation: true
      Tags:
        - Key: %s
          Value: %s
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
    Metadata:
      Description: "Stable name for the %s class's variable key, so an operator reading a key policy or a CloudTrail entry can tell which key it is without resolving the id."
    Properties:
      AliasName: %s
      TargetKeyId: !Ref VarsKey
  VarsTable:
    Type: AWS::DynamoDB::Table
    Metadata:
      Description: "Every variable Ocel holds for the %s class, keyed by pk/sk, with the recent versions behind each value. Deleting it means every variable has to be set again."
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
`, class, varsKeyComponentTagKey, varsKeyComponentTagValue, class, varsKeyAliasFor(class), class, VarsTableIndexName)
}

func varsOutputs() string {
	return fmt.Sprintf(`  %s:
    Description: "DynamoDB table holding every variable set for this class, with its history. Kept apart from the state table so a variable read never touches deploy state."
    Value: !Ref VarsTable
  %s:
    Description: "KMS key every encrypted value of this class is encrypted under. A deploy decrypts through it, and so does each app's execution role."
    Value: !GetAtt VarsKey.Arn
`, outputVarsTable, outputVarsKeyARN)
}
