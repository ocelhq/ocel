package server

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

type countingCFN struct {
	mu        sync.Mutex
	describes int
	stacks    []string
}

func (c *countingCFN) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	c.mu.Lock()
	c.describes++
	c.stacks = append(c.stacks, aws.ToString(in.StackName))
	c.mu.Unlock()

	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		Outputs: []cfntypes.Output{
			{OutputKey: aws.String("BootstrapVersion"), OutputValue: aws.String(bootstrapVersionOutput)},
			{OutputKey: aws.String("VarsTableName"), OutputValue: aws.String("ocel-vars")},
			{OutputKey: aws.String("VarsKeyArn"), OutputValue: aws.String("arn:aws:kms:eu-west-1:123456789012:key/abcd")},
		},
	}}}, nil
}

func (c *countingCFN) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.describes
}

type fakeDynamo struct {
	mu      sync.Mutex
	items   map[string]map[string]map[string]ddbtypes.AttributeValue
	queries int
	gets    int
}

func newFakeDynamo() *fakeDynamo {
	return &fakeDynamo{items: map[string]map[string]map[string]ddbtypes.AttributeValue{}}
}

func keyOf(item map[string]ddbtypes.AttributeValue, name string) string {
	v, _ := item[name].(*ddbtypes.AttributeValueMemberS)
	if v == nil {
		return ""
	}
	return v.Value
}

func (f *fakeDynamo) put(item map[string]ddbtypes.AttributeValue) {
	pk, sk := keyOf(item, "pk"), keyOf(item, "sk")
	if f.items[pk] == nil {
		f.items[pk] = map[string]map[string]ddbtypes.AttributeValue{}
	}
	f.items[pk][sk] = item
}

func (f *fakeDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	return &dynamodb.GetItemOutput{Item: f.items[keyOf(in.Key, "pk")][keyOf(in.Key, "sk")]}, nil
}

func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries++

	pk, _ := in.ExpressionAttributeValues[":pk"].(*ddbtypes.AttributeValueMemberS)
	prefix, _ := in.ExpressionAttributeValues[":prefix"].(*ddbtypes.AttributeValueMemberS)
	if in.IndexName != nil {
		return f.queryIndex(in), nil
	}

	var sks []string
	for sk := range f.items[pk.Value] {
		if strings.HasPrefix(sk, prefix.Value) {
			sks = append(sks, sk)
		}
	}
	sort.Strings(sks)

	out := &dynamodb.QueryOutput{}
	for _, sk := range sks {
		out.Items = append(out.Items, f.items[pk.Value][sk])
	}
	return out, nil
}

func (f *fakeDynamo) queryIndex(in *dynamodb.QueryInput) *dynamodb.QueryOutput {
	gsi1pk, _ := in.ExpressionAttributeValues[":pk"].(*ddbtypes.AttributeValueMemberS)
	gsi1sk, _ := in.ExpressionAttributeValues[":sk"].(*ddbtypes.AttributeValueMemberS)

	out := &dynamodb.QueryOutput{}
	for _, sks := range f.items {
		for _, item := range sks {
			if keyOf(item, "gsi1pk") == gsi1pk.Value && keyOf(item, "gsi1sk") == gsi1sk.Value {
				out.Items = append(out.Items, item)
			}
		}
	}
	return out
}

func (f *fakeDynamo) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, write := range in.TransactItems {
		switch {
		case write.Put != nil:
			f.put(write.Put.Item)
		case write.Delete != nil:
			delete(f.items[keyOf(write.Delete.Key, "pk")], keyOf(write.Delete.Key, "sk"))
		}
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func (f *fakeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put(in.Item)
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDynamo) counts() (queries, gets int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries, f.gets
}

type fakeKMS struct {
	mu       sync.Mutex
	decrypts int
}

const fakeCipherMarker = "enc:"

func (f *fakeKMS) Encrypt(_ context.Context, in *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	return &kms.EncryptOutput{CiphertextBlob: []byte(fakeCipherMarker + string(in.Plaintext))}, nil
}

func (f *fakeKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	f.mu.Lock()
	f.decrypts++
	f.mu.Unlock()
	return &kms.DecryptOutput{Plaintext: []byte(strings.TrimPrefix(string(in.CiphertextBlob), fakeCipherMarker))}, nil
}

func (f *fakeKMS) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.decrypts
}

func testAccount(cfn *countingCFN, ddb *fakeDynamo, crypto *fakeKMS) *VarsServer {
	return &VarsServer{openAccount: func(context.Context, string) (account, error) {
		return account{CFN: cfn, Dynamo: ddb, KMS: crypto}, nil
	}}
}

var bootstrapVersionOutput = strconv.Itoa(bootstrap.RequiredBootstrapVersion)
