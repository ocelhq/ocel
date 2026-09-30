package alb

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

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

	trustStore       string
	trustStatus      elbv2types.TrustStoreStatus
	trustModified    int
	trustBundle      string
	trustTags        map[string]string
	failTrustModify  error
	failTrustCreate  error
	certificateLimit int
	ruleLimit        int
	listenerMode     string
	listenerTrust    string

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
		trustTags:    map[string]string{},
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
	if f.ruleLimit > 0 && len(f.rules) >= f.ruleLimit {
		return nil, &elbv2types.TooManyRulesException{Message: aws.String("You've reached the limit on the number of rules per load balancer")}
	}
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
			if f.certificateLimit > 0 && len(f.certificates) >= f.certificateLimit {
				return nil, &elbv2types.TooManyCertificatesException{Message: aws.String("You've reached the limit on the number of certificates per load balancer")}
			}
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
	if failure := f.failTrustCreate; failure != nil {
		f.failTrustCreate = nil
		return nil, failure
	}
	bundle, held := f.objects[aws.ToString(in.CaCertificatesBundleS3Key)]
	if !held || aws.ToString(in.CaCertificatesBundleS3Bucket) != testBucket {
		return nil, errors.New("the bundle is not in the bucket")
	}
	f.trustBundle = string(bundle)
	for _, tag := range in.Tags {
		f.trustTags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	f.trustStore = "arn:aws:elasticloadbalancing:us-east-1:111122223333:truststore/" + aws.ToString(in.Name) + "/1"
	f.trustStatus = elbv2types.TrustStoreStatusCreating
	return &elbv2.CreateTrustStoreOutput{TrustStores: []elbv2types.TrustStore{{TrustStoreArn: aws.String(f.trustStore), Status: elbv2types.TrustStoreStatusCreating}}}, nil
}

func (f *fakeAWS) ModifyTrustStore(_ context.Context, in *elbv2.ModifyTrustStoreInput, _ ...func(*elbv2.Options)) (*elbv2.ModifyTrustStoreOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if failure := f.failTrustModify; failure != nil {
		f.failTrustModify = nil
		return nil, failure
	}
	bundle, held := f.objects[aws.ToString(in.CaCertificatesBundleS3Key)]
	if !held {
		return nil, &elbv2types.CaCertificatesBundleNotFoundException{Message: in.CaCertificatesBundleS3Key}
	}
	f.trustModified++
	f.trustBundle = string(bundle)
	return &elbv2.ModifyTrustStoreOutput{}, nil
}

func (f *fakeAWS) DescribeTags(_ context.Context, in *elbv2.DescribeTagsInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTagsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var described []elbv2types.TagDescription
	for _, arn := range in.ResourceArns {
		if arn != f.trustStore {
			continue
		}
		var tags []elbv2types.Tag
		for key, value := range f.trustTags {
			tags = append(tags, elbv2types.Tag{Key: aws.String(key), Value: aws.String(value)})
		}
		described = append(described, elbv2types.TagDescription{ResourceArn: aws.String(arn), Tags: tags})
	}
	return &elbv2.DescribeTagsOutput{TagDescriptions: described}, nil
}

func (f *fakeAWS) AddTags(_ context.Context, in *elbv2.AddTagsInput, _ ...func(*elbv2.Options)) (*elbv2.AddTagsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if slices.Contains(in.ResourceArns, f.trustStore) {
		for _, tag := range in.Tags {
			f.trustTags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
	}
	return &elbv2.AddTagsOutput{}, nil
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

func (f *fakeAWS) trusted() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.trustBundle
}

func mintCA(t *testing.T, name string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func mintLeaf(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "your own"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
