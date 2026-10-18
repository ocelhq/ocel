package deploy

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type fakeParameters struct {
	mu       sync.Mutex
	values   map[string]string
	types    map[string]ssmtypes.ParameterType
	puts     int
	listings int
	deleted  []string

	beforePut func(name string)
}

func newFakeParameters() *fakeParameters {
	return &fakeParameters{values: map[string]string{}, types: map[string]ssmtypes.ParameterType{}}
}

func (f *fakeParameters) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, found := f.values[aws.ToString(in.Name)]
	if !found {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Name: in.Name, Value: aws.String(value)}}, nil
}

func (f *fakeParameters) PutParameter(_ context.Context, in *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	name := aws.ToString(in.Name)
	if f.beforePut != nil {
		f.beforePut(name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	if _, exists := f.values[name]; exists && !aws.ToBool(in.Overwrite) {
		return nil, &ssmtypes.ParameterAlreadyExists{}
	}
	f.values[name] = aws.ToString(in.Value)
	f.types[name] = in.Type
	return &ssm.PutParameterOutput{}, nil
}

func (f *fakeParameters) DeleteParameter(_ context.Context, in *ssm.DeleteParameterInput, _ ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	if _, found := f.values[name]; !found {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	delete(f.values, name)
	f.deleted = append(f.deleted, name)
	return &ssm.DeleteParameterOutput{}, nil
}

func (f *fakeParameters) GetParametersByPath(_ context.Context, in *ssm.GetParametersByPathInput, _ ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listings++
	path := strings.TrimSuffix(aws.ToString(in.Path), "/") + "/"
	var names []string
	for name := range f.values {
		if rest, under := strings.CutPrefix(name, path); under && rest != "" && !strings.Contains(rest, "/") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	page := int(aws.ToInt32(in.MaxResults))
	if page == 0 || page > 10 {
		page = 10
	}
	start := 0
	if in.NextToken != nil {
		start, _ = strconv.Atoi(*in.NextToken)
	}
	end := min(start+page, len(names))
	out := &ssm.GetParametersByPathOutput{}
	for _, name := range names[start:end] {
		out.Parameters = append(out.Parameters, ssmtypes.Parameter{Name: aws.String(name)})
	}
	if end < len(names) {
		out.NextToken = aws.String(strconv.Itoa(end))
	}
	return out, nil
}

var mintedToken = regexp.MustCompile(`^[A-Za-z0-9]{64}$`)
