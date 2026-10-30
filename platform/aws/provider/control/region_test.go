package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type regionsCloud struct {
	offered map[string]string
	denied  bool

	rejectsNames bool

	mu      sync.Mutex
	reached []string
}

func (c *regionsCloud) Do(req *http.Request) (*http.Response, error) {
	service, rest, _ := strings.Cut(req.URL.Host, ".")
	named := regionNamesIn(req)
	c.mu.Lock()
	if service == "ec2" {
		c.reached = append(c.reached, req.URL.Host+":"+strings.Join(named, ","))
	} else {
		c.reached = append(c.reached, req.URL.Host)
	}
	c.mu.Unlock()
	region, _, _ := strings.Cut(rest, ".amazonaws.com")
	status, offered := c.offered[region]
	switch {
	case !offered:
		return nil, &net.DNSError{Err: "no such host", Name: req.URL.Host, IsNotFound: true}
	case service != "ec2":
		return answer(http.StatusOK, `<GetCallerIdentityResponse><GetCallerIdentityResult><Arn>arn:aws:iam::111122223333:user/dev</Arn><Account>111122223333</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`), nil
	case c.denied:
		return answer(http.StatusForbidden, `<Response><Errors><Error><Code>UnauthorizedOperation</Code><Message>not authorized to perform: ec2:DescribeRegions</Message></Error></Errors></Response>`), nil
	case status == "not-opted-in":
		return answer(http.StatusUnauthorized, `<Response><Errors><Error><Code>AuthFailure</Code><Message>AWS was not able to validate the provided access credentials</Message></Error></Errors></Response>`), nil
	}
	if len(named) > 0 && c.rejectsNames {
		return answer(http.StatusBadRequest, `<Response><Errors><Error><Code>InvalidParameterValue</Code><Message>RegionName may not be given</Message></Error></Errors></Response>`), nil
	}
	if len(named) == 0 {
		named = slices.Sorted(maps.Keys(c.offered))
	}
	var items strings.Builder
	for _, name := range named {
		if _, offered := c.offered[name]; !offered {
			return answer(http.StatusBadRequest, `<Response><Errors><Error><Code>InvalidParameterValue</Code><Message>Invalid region: `+name+`</Message></Error></Errors></Response>`), nil
		}
		fmt.Fprintf(&items, "<item><regionName>%s</regionName><optInStatus>%s</optInStatus></item>", name, c.offered[name])
	}
	return answer(http.StatusOK, "<DescribeRegionsResponse><regionInfo>"+items.String()+"</regionInfo></DescribeRegionsResponse>"), nil
}

func regionNamesIn(req *http.Request) []string {
	if req.Body == nil {
		return nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil
	}
	var named []string
	for i := 1; form.Has(fmt.Sprintf("RegionName.%d", i)); i++ {
		named = append(named, form.Get(fmt.Sprintf("RegionName.%d", i)))
	}
	return named
}

func (c *regionsCloud) asked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var asked []string
	for _, reached := range c.reached {
		if strings.HasPrefix(reached, "ec2.") {
			asked = append(asked, reached)
		}
	}
	return asked
}

func (c *regionsCloud) reachedSTS() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(c.reached, func(host string) bool { return strings.HasPrefix(host, "sts.") })
}

func answer(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"text/xml"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func commercialCloud() *regionsCloud {
	return &regionsCloud{offered: map[string]string{
		"eu-west-1":  "opt-in-not-required",
		"eu-west-2":  "opt-in-not-required",
		"us-east-1":  "opt-in-not-required",
		"af-south-1": "not-opted-in",
	}}
}

func configIn(cloud *regionsCloud, region string) aws.Config {
	return aws.Config{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
		HTTPClient:  cloud,
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
	}
}

func askCallerIdentity(cfg aws.Config) error {
	_, err := sts.NewFromConfig(cfg).GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{})
	return err
}

func refusedBeforeAnyCall(t *testing.T, cloud *regionsCloud, region string, code refusal.Code) refusal.Refusal {
	t.Helper()
	err := askCallerIdentity(addRegionCheck(configIn(cloud, region), region))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != code || !refusal.IsOption(err) {
		t.Fatalf("a call in %q = %v, want a %s refusal of the option", region, err, code)
	}
	if cloud.reachedSTS() {
		t.Errorf("a call in %q reached STS, want it refused before anything scoped to the region is called", region)
	}
	return refused
}

func TestAMistypedRegionIsRefusedBeforeAnyCallNamingTheOneMeant(t *testing.T) {
	t.Parallel()

	cloud := commercialCloud()

	refused := refusedBeforeAnyCall(t, cloud, "eu-wset-1", refusal.CodeInvalid)

	want := `option "region" names "eu-wset-1", which is not a region AWS offers. Did you mean "eu-west-1"? ` +
		"Regions: af-south-1, eu-west-1, eu-west-2, us-east-1. See https://docs.aws.amazon.com/global-infrastructure/latest/regions/aws-regions.html"
	if refused.Message != want {
		t.Errorf("refused with\n%s\nwant\n%s", refused.Message, want)
	}
	asked := []string{"ec2.eu-wset-1.amazonaws.com:eu-wset-1", "ec2.us-east-1.amazonaws.com:eu-wset-1", "ec2.us-east-1.amazonaws.com:"}
	if got := cloud.asked(); !slices.Equal(got, asked) {
		t.Errorf("the regions were asked as %v, want %v: eu-wset-1 asked of by name, and every region listed only to say which was meant", got, asked)
	}
}

func TestARegionFromTheEnvironmentIsRefusedNamingTheVariableThatNamesIt(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "eu-wset-1")

	err := askCallerIdentity(addRegionCheck(configIn(commercialCloud(), "eu-wset-1"), ""))

	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.HasPrefix(refused.Message, `AWS_DEFAULT_REGION names "eu-wset-1", which is not a region AWS offers.`) {
		t.Errorf("a call in eu-wset-1 from AWS_DEFAULT_REGION = %v, want the refusal to name AWS_DEFAULT_REGION, as no option named the region", err)
	}
}

func TestAnAvailabilityZoneIsRefusedNamingTheRegionItSitsIn(t *testing.T) {
	t.Parallel()

	refused := refusedBeforeAnyCall(t, commercialCloud(), "us-east-1a", refusal.CodeInvalid)

	if !strings.Contains(refused.Message, `Did you mean "us-east-1"?`) {
		t.Errorf("refused with %q, want it to suggest us-east-1", refused.Message)
	}
}

func TestARegionTheAccountHasNotEnabledIsRefusedSayingToEnableIt(t *testing.T) {
	t.Parallel()

	refused := refusedBeforeAnyCall(t, commercialCloud(), "af-south-1", refusal.CodeNotReady)

	want := `option "region" names "af-south-1", a region this account has not enabled. ` +
		"Enable it, then try again: https://docs.aws.amazon.com/accounts/latest/reference/manage-acct-regions.html"
	if refused.Message != want {
		t.Errorf("refused with\n%s\nwant\n%s", refused.Message, want)
	}
}

func TestAnEnabledRegionIsCheckedOnceAndInItself(t *testing.T) {
	t.Parallel()

	cloud := commercialCloud()
	cfg := addRegionCheck(configIn(cloud, "eu-west-1"), "eu-west-1")

	for range 2 {
		if err := askCallerIdentity(cfg); err != nil {
			t.Fatalf("a call in eu-west-1 = %v, want it to pass", err)
		}
	}
	if asked := cloud.asked(); !slices.Equal(asked, []string{"ec2.eu-west-1.amazonaws.com:eu-west-1"}) {
		t.Errorf("the regions were asked as %v, want eu-west-1 asked of by name, once, at its own endpoint, which an organization limiting its regions still admits", asked)
	}
}

func TestAMistypedChinaRegionIsCheckedAgainstChinasRegions(t *testing.T) {
	t.Parallel()

	cloud := &regionsCloud{offered: map[string]string{"cn-north-1": "opt-in-not-required", "cn-northwest-1": "opt-in-not-required"}}

	refused := refusedBeforeAnyCall(t, cloud, "cn-nort-1", refusal.CodeInvalid)

	if !strings.Contains(refused.Message, `Did you mean "cn-north-1"?`) {
		t.Errorf("refused with %q, want it to suggest cn-north-1", refused.Message)
	}
	if asked := cloud.asked(); !slices.Contains(asked, "ec2.cn-northwest-1.amazonaws.com.cn:cn-nort-1") {
		t.Errorf("the regions were asked of %v, want China's own endpoint: no other partition answers for a China account", asked)
	}
}

func TestARegionAWSListsIsAdmittedEvenWhereAskingForItByNameIsRejected(t *testing.T) {
	t.Parallel()

	cloud := commercialCloud()
	cloud.rejectsNames = true

	if err := askCallerIdentity(addRegionCheck(configIn(cloud, "eu-west-1"), "eu-west-1")); err != nil {
		t.Errorf("a call in eu-west-1 = %v, want it made: a region is refused only when the list of every region lacks it too", err)
	}
}

func TestACredentialThatMayNotListRegionsCallsAsBefore(t *testing.T) {
	t.Parallel()

	cloud := commercialCloud()
	cloud.denied = true

	if err := askCallerIdentity(addRegionCheck(configIn(cloud, "eu-west-1"), "eu-west-1")); err != nil {
		t.Errorf("a call in eu-west-1 = %v, want it made: what the check cannot learn it leaves to the call", err)
	}
}

func TestAnEmulatedCloudIsNeverAskedForItsRegions(t *testing.T) {
	t.Parallel()

	cloud := commercialCloud()
	cfg := configIn(cloud, "eu-west-1")
	cfg.BaseEndpoint = aws.String("http://127.0.0.1:4566")

	_ = askCallerIdentity(addRegionCheck(cfg, "eu-west-1"))
	if asked := cloud.asked(); len(asked) != 0 {
		t.Errorf("the regions were asked of %v, want an emulator left alone", asked)
	}
}

func TestWhoamiInAMistypedRegionIsRefusedForTheRegionRatherThanTheCredential(t *testing.T) {
	t.Parallel()

	credentials := CredentialsFor(addRegionCheck(configIn(commercialCloud(), "eu-wset-1"), "eu-wset-1"), defaultNamespace, "")

	_, err := credentials.Whoami(context.Background())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !refusal.IsOption(err) ||
		!strings.HasPrefix(refused.Message, `option "region" names "eu-wset-1", which is not a region AWS offers.`) {
		t.Errorf("Whoami() in eu-wset-1 = %v, want the region option refused as it is, which preflight reports as that refusal rather than as the credential's", err)
	}
}
