package bootstrap

import (
	"fmt"

	"github.com/ocelhq/ocel/cloud/aws/vars"
)

const (
	// VarsTableIndexName is the variables table's secondary index: the reverse
	// lookup answering "what references this value" before an edit changes it.
	// It is sparse, so only reference items are indexed at all. The store names
	// it too, and provisioning it under one name while querying another is a
	// failure nothing would catch until the first lookup.
	VarsTableIndexName = vars.IndexName

	outputVarsTable  = "VarsTableName"
	outputVarsKeyARN = "VarsKeyArn"
)

// varsKeyAliasFor names the class's variable key. It carries the class so both
// substrates can be bootstrapped into one account, and so an operator auditing
// key usage can attribute a decrypt without resolving a key id.
func varsKeyAliasFor(class string) string {
	return "alias/ocel-vars-" + class
}

// varsResources renders the variable store one substrate provisions: its own
// table, deliberately not the state table so read access to values can be
// granted and audited on its own, and the key those values encrypt under.
//
// The key is per class and never per project, which is the whole of the
// isolation argument: preview compute holds decrypt on the preview key only and
// so cannot read production ciphertext, while a value referenced across projects
// still decrypts under a key both sides hold.
//
// The table sets no SSESpecification: DynamoDB encrypts at rest by default, and
// value confidentiality is application-level envelope encryption under the key
// above rather than a property of the table.
//
// The block is a Resources child, emitted before the template's Outputs: line.
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

// varsOutputs renders the coordinates everything downstream reaches the store
// by, resolved from the stack rather than derived.
func varsOutputs() string {
	return fmt.Sprintf(`  %s:
    Description: DynamoDB table holding variable values, separate from general state.
    Value: !Ref VarsTable
  %s:
    Description: KMS key every encrypted value of this class is encrypted under.
    Value: !GetAtt VarsKey.Arn
`, outputVarsTable, outputVarsKeyARN)
}
