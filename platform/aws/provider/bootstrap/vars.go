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
      Description: "Ocel: the key every encrypted variable of the %s class is encrypted under. Ocel encrypts a value here when it is set, a deploy decrypts the ones it bakes into an app, and a running function decrypts the ones it reads live. Nothing else in this account uses it. Scheduling its deletion does not remove the values, it strands them: every encrypted variable of this class becomes unreadable to Ocel and to the apps holding live references, and each has to be set again against a new key."
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
    Metadata:
      Description: "Stable name for the %s class's variable key, so an operator reading a key policy or a CloudTrail entry can tell which key this is without resolving its id. Nothing reads variables through the alias - Ocel is handed the key ARN by the stack output. Deleting the alias leaves the key and every value encrypted under it intact, and the next bootstrap recreates it."
    Properties:
      AliasName: %s
      TargetKeyId: !Ref VarsKey
  VarsTable:
    Type: AWS::DynamoDB::Table
    Metadata:
      Description: "Every variable Ocel holds for the %s class, keyed by pk/sk: the current value of each variable and the recent versions behind it, plaintext for plain values and ciphertext for encrypted ones, which only VarsKey can open. A deploy reads it to resolve the variables an app references, and a function reads the ones declared live on every invocation. Deleting this table loses every variable set for this class - already-deployed apps keep the values baked into them, but a live read starts failing and no deploy can resolve a reference until each value is set again."
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
`, class, class, varsKeyAliasFor(class), class, VarsTableIndexName)
}

func varsOutputs() string {
	return fmt.Sprintf(`  %s:
    Description: "DynamoDB table holding every variable set for this class, with its history: the values a deploy resolves references against and a function reads live. Kept apart from the state table so a variable read never touches deploy state."
    Value: !Ref VarsTable
  %s:
    Description: "KMS key every encrypted value of this class is encrypted under. Ocel decrypts through it to bake a value into a deploy, and each app's execution role is granted it so a function can open the values it reads live."
    Value: !GetAtt VarsKey.Arn
`, outputVarsTable, outputVarsKeyARN)
}
