package server

import (
	"context"
	"fmt"
	"maps"
	"slices"
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

func (c *countingCFN) substrates() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return substrateDescribes(c.stacks)
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

	if in.IndexName != nil {
		return f.queryIndex(in), nil
	}
	pk := keyOf(in.ExpressionAttributeValues, ":pk")
	prefix := keyOf(in.ExpressionAttributeValues, ":prefix")

	var sks []string
	for sk := range f.items[pk] {
		if strings.HasPrefix(sk, prefix) {
			sks = append(sks, sk)
		}
	}
	slices.Sort(sks)

	out := &dynamodb.QueryOutput{}
	for _, sk := range sks {
		out.Items = append(out.Items, f.items[pk][sk])
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
		case write.Update != nil:
			updated, err := f.applyUpdate(write.Update)
			if err != nil {
				return nil, err
			}
			f.put(updated)
		}
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func (f *fakeDynamo) applyUpdate(u *ddbtypes.Update) (map[string]ddbtypes.AttributeValue, error) {
	item := map[string]ddbtypes.AttributeValue{}
	maps.Copy(item, f.items[keyOf(u.Key, "pk")][keyOf(u.Key, "sk")])
	maps.Copy(item, u.Key)

	sets, adds, added := strings.Cut(aws.ToString(u.UpdateExpression), " ADD ")
	assignments := map[string][]string{"SET": strings.Split(strings.TrimPrefix(sets, "SET "), ", ")}
	if added {
		assignments["ADD"] = strings.Split(adds, ", ")
	}
	for clause, clauseAssignments := range assignments {
		separator := " "
		if clause == "SET" {
			separator = " = "
		}
		for _, assignment := range clauseAssignments {
			alias, placeholder, ok := strings.Cut(assignment, separator)
			if !ok {
				return nil, fmt.Errorf("fakeDynamo: %s clause %q is not one this double understands", clause, assignment)
			}
			name, known := u.ExpressionAttributeNames[alias]
			value, carried := u.ExpressionAttributeValues[placeholder]
			if !known || !carried {
				return nil, fmt.Errorf("fakeDynamo: %q or %q names nothing", alias, placeholder)
			}
			if clause == "ADD" {
				value = &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(numberOf(item, name)+numberOf(u.ExpressionAttributeValues, placeholder), 10)}
			}
			item[name] = value
		}
	}
	return item, nil
}

func numberOf(m map[string]ddbtypes.AttributeValue, name string) int64 {
	v, ok := m[name].(*ddbtypes.AttributeValueMemberN)
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(v.Value, 10, 64)
	if err != nil {
		return 0
	}
	return n
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
	return &VarsServer{stores: testStores(cfn, ddb, crypto)}
}

func testStores(cfn *countingCFN, ddb *fakeDynamo, crypto *fakeKMS) *stores {
	return &stores{openAccount: func(context.Context, string) (account, error) {
		return account{CFN: cfn, Dynamo: ddb, KMS: crypto}, nil
	}}
}

var bootstrapVersionOutput = strconv.Itoa(bootstrap.RequiredBootstrapVersion)
