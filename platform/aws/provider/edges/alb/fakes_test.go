package alb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	testAddress  = "ocel-cloudflare-production-1234.us-east-1.elb.amazonaws.com"
	testListener = "arn:aws:elasticloadbalancing:us-east-1:111122223333:listener/app/ocel-cloudflare-production/abc/def"
	testBucket   = "ocel-artifacts"
)

type fakeRule struct {
	priority int32
	host     string
	actions  []elbv2types.Action
}

type fakeAWS struct {
	mu sync.Mutex

	absent       bool
	rules        map[string]*fakeRule
	nextRule     int
	certificates []string
	failModify   error

	trustStore    string
	trustStatus   elbv2types.TrustStoreStatus
	trustModified int
	listenerMode  string
	listenerTrust string

	objects map[string][]byte
	etags   map[string]int

	services map[string]string
	builds   map[string]string
	calls    map[string]int
}

func newFakeAWS() *fakeAWS {
	return &fakeAWS{
		rules:        map[string]*fakeRule{},
		objects:      map[string][]byte{},
		etags:        map[string]int{},
		services:     map[string]string{},
		builds:       map[string]string{},
		calls:        map[string]int{},
		listenerMode: "off",
	}
}

func (f *fakeAWS) clients(context.Context, environment.Tier) (Clients, error) {
	return Clients{Balancers: f, Services: f, Objects: f, Bucket: testBucket}, nil
}

func (f *fakeAWS) release(physical, build string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	group := "arn:aws:elasticloadbalancing:us-east-1:111122223333:targetgroup/ocel-" + build + "/1"
	f.services[physical] = group
	f.builds[group] = build
}

func (f *fakeAWS) servedBy(host string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rule := range f.rules {
		if rule.host != host {
			continue
		}
		for _, action := range rule.actions {
			if action.Type == elbv2types.ActionTypeEnumForward {
				return f.builds[aws.ToString(action.TargetGroupArn)]
			}
		}
	}
	return ""
}

func (f *fakeAWS) count(call string) {
	f.calls[call]++
}

func (f *fakeAWS) DescribeLoadBalancers(_ context.Context, in *elbv2.DescribeLoadBalancersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("DescribeLoadBalancers")
	if f.absent {
		return nil, &elbv2types.LoadBalancerNotFoundException{Message: aws.String("not found")}
	}
	return &elbv2.DescribeLoadBalancersOutput{LoadBalancers: []elbv2types.LoadBalancer{{
		LoadBalancerArn:  aws.String("arn:aws:elasticloadbalancing:us-east-1:111122223333:loadbalancer/app/" + in.Names[0] + "/abc"),
		LoadBalancerName: aws.String(in.Names[0]),
		DNSName:          aws.String(testAddress),
	}}}, nil
}

func (f *fakeAWS) DescribeListeners(context.Context, *elbv2.DescribeListenersInput, ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("DescribeListeners")
	return &elbv2.DescribeListenersOutput{Listeners: []elbv2types.Listener{
		{ListenerArn: aws.String(testListener + "-liveness"), Port: aws.Int32(8443)},
		{ListenerArn: aws.String(testListener), Port: aws.Int32(443), MutualAuthentication: &elbv2types.MutualAuthenticationAttributes{
			Mode: aws.String(f.listenerMode), TrustStoreArn: aws.String(f.listenerTrust),
		}},
	}}, nil
}

func (f *fakeAWS) DescribeRules(_ context.Context, in *elbv2.DescribeRulesInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeRulesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("DescribeRules")
	var out []elbv2types.Rule
	for _, arn := range in.RuleArns {
		rule, found := f.rules[arn]
		if !found {
			return nil, &elbv2types.RuleNotFoundException{Message: aws.String(arn)}
		}
		out = append(out, elbv2types.Rule{RuleArn: aws.String(arn), Priority: aws.String(strconv.Itoa(int(rule.priority))), Actions: slices.Clone(rule.actions)})
	}
	return &elbv2.DescribeRulesOutput{Rules: out}, nil
}

func (f *fakeAWS) CreateRule(_ context.Context, in *elbv2.CreateRuleInput, _ ...func(*elbv2.Options)) (*elbv2.CreateRuleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("CreateRule")
	for _, rule := range f.rules {
		if rule.priority == aws.ToInt32(in.Priority) {
			return nil, &elbv2types.PriorityInUseException{Message: aws.String("taken")}
		}
	}
	f.nextRule++
	arn := fmt.Sprintf("%s/rule-%d", testListener, f.nextRule)
	f.rules[arn] = &fakeRule{priority: aws.ToInt32(in.Priority), host: in.Conditions[0].HostHeaderConfig.Values[0], actions: slices.Clone(in.Actions)}
	return &elbv2.CreateRuleOutput{Rules: []elbv2types.Rule{{RuleArn: aws.String(arn)}}}, nil
}

func (f *fakeAWS) ModifyRule(_ context.Context, in *elbv2.ModifyRuleInput, _ ...func(*elbv2.Options)) (*elbv2.ModifyRuleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("ModifyRule")
	if failure := f.failModify; failure != nil {
		f.failModify = nil
		return nil, failure
	}
	rule, found := f.rules[aws.ToString(in.RuleArn)]
	if !found {
		return nil, &elbv2types.RuleNotFoundException{Message: in.RuleArn}
	}
	rule.actions = slices.Clone(in.Actions)
	return &elbv2.ModifyRuleOutput{}, nil
}

func (f *fakeAWS) DeleteRule(_ context.Context, in *elbv2.DeleteRuleInput, _ ...func(*elbv2.Options)) (*elbv2.DeleteRuleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("DeleteRule")
	if _, found := f.rules[aws.ToString(in.RuleArn)]; !found {
		return nil, &elbv2types.RuleNotFoundException{Message: in.RuleArn}
	}
	delete(f.rules, aws.ToString(in.RuleArn))
	return &elbv2.DeleteRuleOutput{}, nil
}

func (f *fakeAWS) AddListenerCertificates(_ context.Context, in *elbv2.AddListenerCertificatesInput, _ ...func(*elbv2.Options)) (*elbv2.AddListenerCertificatesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, certificate := range in.Certificates {
		if arn := aws.ToString(certificate.CertificateArn); !slices.Contains(f.certificates, arn) {
			f.certificates = append(f.certificates, arn)
		}
	}
	return &elbv2.AddListenerCertificatesOutput{}, nil
}

func (f *fakeAWS) RemoveListenerCertificates(_ context.Context, in *elbv2.RemoveListenerCertificatesInput, _ ...func(*elbv2.Options)) (*elbv2.RemoveListenerCertificatesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, certificate := range in.Certificates {
		f.certificates = slices.DeleteFunc(f.certificates, func(held string) bool { return held == aws.ToString(certificate.CertificateArn) })
	}
	return &elbv2.RemoveListenerCertificatesOutput{}, nil
}

func (f *fakeAWS) DescribeTrustStores(_ context.Context, in *elbv2.DescribeTrustStoresInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTrustStoresOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.trustStore == "" {
		return nil, &elbv2types.TrustStoreNotFoundException{Message: aws.String("none")}
	}
	status := f.trustStatus
	f.trustStatus = elbv2types.TrustStoreStatusActive
	return &elbv2.DescribeTrustStoresOutput{TrustStores: []elbv2types.TrustStore{{TrustStoreArn: aws.String(f.trustStore), Name: aws.String(in.Names[0]), Status: status}}}, nil
}

func (f *fakeAWS) CreateTrustStore(_ context.Context, in *elbv2.CreateTrustStoreInput, _ ...func(*elbv2.Options)) (*elbv2.CreateTrustStoreOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, held := f.objects[aws.ToString(in.CaCertificatesBundleS3Key)]; !held || aws.ToString(in.CaCertificatesBundleS3Bucket) != testBucket {
		return nil, errors.New("the bundle is not in the bucket")
	}
	f.trustStore = "arn:aws:elasticloadbalancing:us-east-1:111122223333:truststore/" + aws.ToString(in.Name) + "/1"
	f.trustStatus = elbv2types.TrustStoreStatusCreating
	return &elbv2.CreateTrustStoreOutput{TrustStores: []elbv2types.TrustStore{{TrustStoreArn: aws.String(f.trustStore), Status: elbv2types.TrustStoreStatusCreating}}}, nil
}

func (f *fakeAWS) ModifyTrustStore(context.Context, *elbv2.ModifyTrustStoreInput, ...func(*elbv2.Options)) (*elbv2.ModifyTrustStoreOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trustModified++
	return &elbv2.ModifyTrustStoreOutput{}, nil
}

func (f *fakeAWS) ModifyListener(_ context.Context, in *elbv2.ModifyListenerInput, _ ...func(*elbv2.Options)) (*elbv2.ModifyListenerOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if mutual := in.MutualAuthentication; mutual != nil {
		f.listenerMode, f.listenerTrust = aws.ToString(mutual.Mode), aws.ToString(mutual.TrustStoreArn)
	}
	return &elbv2.ModifyListenerOutput{}, nil
}

func (f *fakeAWS) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("DescribeServices")
	out := &ecs.DescribeServicesOutput{}
	for _, name := range in.Services {
		group, found := f.services[name]
		if !found {
			out.Failures = append(out.Failures, ecstypes.Failure{Arn: aws.String(name), Reason: aws.String("MISSING")})
			continue
		}
		out.Services = append(out.Services, ecstypes.Service{ServiceName: aws.String(name), LoadBalancers: []ecstypes.LoadBalancer{{TargetGroupArn: aws.String(group)}}})
	}
	return out, nil
}

type preconditionFailed struct{}

func (preconditionFailed) Error() string                 { return "PreconditionFailed" }
func (preconditionFailed) ErrorCode() string             { return "PreconditionFailed" }
func (preconditionFailed) ErrorMessage() string          { return "the object changed" }
func (preconditionFailed) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func (f *fakeAWS) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, found := f.objects[aws.ToString(in.Key)]
	if !found {
		return nil, &s3types.NoSuchKey{Message: aws.String("absent")}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body)), ETag: aws.String(strconv.Itoa(f.etags[aws.ToString(in.Key)]))}, nil
}

func (f *fakeAWS) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := aws.ToString(in.Key)
	_, found := f.objects[key]
	switch {
	case in.IfNoneMatch != nil && found:
		return nil, preconditionFailed{}
	case in.IfMatch != nil && (!found || aws.ToString(in.IfMatch) != strconv.Itoa(f.etags[key])):
		return nil, preconditionFailed{}
	}
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.objects[key] = body
	f.etags[key]++
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeAWS) heldCertificates() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.certificates)
}

func (f *fakeAWS) ruleHosts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var hosts []string
	for _, rule := range f.rules {
		hosts = append(hosts, rule.host)
	}
	slices.Sort(hosts)
	return hosts
}

func (f *fakeAWS) bundle() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, key := range slices.Sorted(maps.Keys(f.objects)) {
		return string(f.objects[key])
	}
	return ""
}
