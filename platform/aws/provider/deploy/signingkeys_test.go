package deploy

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

type fakeSigningKeys struct {
	mu       sync.Mutex
	values   map[string]string
	tags     map[string][]smtypes.Tag
	creates  int
	listings int
	deleted  []string
	forced   []bool

	beforeCreate func(name string)
}

func newFakeSigningKeys() *fakeSigningKeys {
	return &fakeSigningKeys{values: map[string]string{}, tags: map[string][]smtypes.Tag{}}
}

func (f *fakeSigningKeys) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, found := f.values[aws.ToString(in.SecretId)]
	if !found {
		return nil, &smtypes.ResourceNotFoundException{}
	}
	return &secretsmanager.GetSecretValueOutput{Name: in.SecretId, SecretString: aws.String(value)}, nil
}

func (f *fakeSigningKeys) CreateSecret(_ context.Context, in *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	name := aws.ToString(in.Name)
	if f.beforeCreate != nil {
		f.beforeCreate(name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if _, exists := f.values[name]; exists {
		return nil, &smtypes.ResourceExistsException{}
	}
	f.values[name] = aws.ToString(in.SecretString)
	f.tags[name] = in.Tags
	return &secretsmanager.CreateSecretOutput{Name: in.Name}, nil
}

func (f *fakeSigningKeys) DeleteSecret(_ context.Context, in *secretsmanager.DeleteSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.SecretId)
	if _, found := f.values[name]; !found {
		return nil, &smtypes.ResourceNotFoundException{}
	}
	delete(f.values, name)
	f.deleted = append(f.deleted, name)
	f.forced = append(f.forced, aws.ToBool(in.ForceDeleteWithoutRecovery))
	return &secretsmanager.DeleteSecretOutput{}, nil
}

func (f *fakeSigningKeys) ListSecrets(_ context.Context, in *secretsmanager.ListSecretsInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listings++
	var prefixes []string
	for _, filter := range in.Filters {
		if filter.Key == smtypes.FilterNameStringTypeName {
			prefixes = append(prefixes, filter.Values...)
		}
	}
	var names []string
	for name := range f.values {
		if slices.ContainsFunc(prefixes, func(prefix string) bool {
			return strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix))
		}) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	start := 0
	if in.NextToken != nil {
		start, _ = strconv.Atoi(*in.NextToken)
	}
	end := min(start+2, len(names))
	out := &secretsmanager.ListSecretsOutput{}
	for _, name := range names[start:end] {
		out.SecretList = append(out.SecretList, smtypes.SecretListEntry{Name: aws.String(name)})
	}
	if end < len(names) {
		out.NextToken = aws.String(strconv.Itoa(end))
	}
	return out, nil
}
