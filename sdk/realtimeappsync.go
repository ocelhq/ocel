package ocel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	appSyncService        = "appsync"
	appSyncPublishPath    = "/event"
	appSyncAPIHostLabel   = ".appsync-api."
	sigV4Algorithm        = "AWS4-HMAC-SHA256"
	sigV4TimeFormat       = "20060102T150405Z"
	sigV4DateFormat       = "20060102"
	containerCredentialIP = "http://169.254.170.2"
	credentialsRefreshGap = 5 * time.Minute
)

var realtimeHTTPClient = http.DefaultClient

var containerCredentialIPs = []net.IP{
	net.IPv4(169, 254, 170, 2),
	net.IPv4(169, 254, 170, 23),
	net.ParseIP("fd00:ec2::23"),
}

type awsCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

type containerCredentials struct {
	mu      sync.Mutex
	held    awsCredentials
	expires time.Time
}

var heldContainerCredentials containerCredentials

func forgetContainerCredentials() {
	heldContainerCredentials.mu.Lock()
	defer heldContainerCredentials.mu.Unlock()
	heldContainerCredentials.held, heldContainerCredentials.expires = awsCredentials{}, time.Time{}
}

func readAWSCredentials(ctx context.Context) (awsCredentials, error) {
	if id, secret := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"); id != "" && secret != "" {
		return awsCredentials{AccessKeyID: id, SecretAccessKey: secret, SessionToken: os.Getenv("AWS_SESSION_TOKEN")}, nil
	}
	if relative := os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"); relative != "" {
		return heldContainerCredentials.read(ctx, containerCredentialIP+relative)
	}
	endpoint := os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")
	if endpoint == "" {
		return awsCredentials{}, fmt.Errorf("no AWS credentials to sign an AppSync publish with: set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, or run where AWS_CONTAINER_CREDENTIALS_RELATIVE_URI is delivered")
	}
	if err := refuseUntrustedCredentialsEndpoint(endpoint); err != nil {
		return awsCredentials{}, err
	}
	return heldContainerCredentials.read(ctx, endpoint)
}

func refuseUntrustedCredentialsEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("AWS_CONTAINER_CREDENTIALS_FULL_URI is no URL: %w", err)
	}
	if parsed.Scheme == "https" || parsed.Scheme == "http" && isContainerCredentialsHost(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("AWS_CONTAINER_CREDENTIALS_FULL_URI names %s://%s, and only https or a loopback, ECS or EKS address over http is asked for credentials", parsed.Scheme, parsed.Host)
}

func isContainerCredentialsHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, allowed := range containerCredentialIPs {
		if ip.Equal(allowed) {
			return true
		}
	}
	return ip.IsLoopback()
}

func (c *containerCredentials) read(ctx context.Context, endpoint string) (awsCredentials, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held.AccessKeyID != "" && time.Until(c.expires) > credentialsRefreshGap {
		return c.held, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return awsCredentials{}, err
	}
	token := os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN")
	if file := os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"); file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return awsCredentials{}, fmt.Errorf("read the container credentials token: %w", err)
		}
		token = strings.TrimSpace(string(raw))
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	res, err := realtimeHTTPClient.Do(req)
	if err != nil {
		return awsCredentials{}, fmt.Errorf("read the container's AWS credentials: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return awsCredentials{}, fmt.Errorf("read the container's AWS credentials: status %d", res.StatusCode)
	}
	var answered struct {
		AccessKeyID     string    `json:"AccessKeyId"`
		SecretAccessKey string    `json:"SecretAccessKey"`
		Token           string    `json:"Token"`
		Expiration      time.Time `json:"Expiration"`
	}
	if err := json.NewDecoder(res.Body).Decode(&answered); err != nil || answered.AccessKeyID == "" {
		return awsCredentials{}, fmt.Errorf("the container's credentials endpoint answered no AWS credentials")
	}
	c.held = awsCredentials{AccessKeyID: answered.AccessKeyID, SecretAccessKey: answered.SecretAccessKey, SessionToken: answered.Token}
	c.expires = answered.Expiration
	return c.held, nil
}

func findAppSyncRegion(host string) string {
	if _, rest, found := strings.Cut(host, appSyncAPIHostLabel); found {
		if region, _, found := strings.Cut(rest, "."); found && region != "" {
			return region
		}
	}
	return os.Getenv("AWS_REGION")
}

func hashSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func signHMAC(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func signAppSyncPublish(host string, body []byte, creds awsCredentials, region string, at time.Time) http.Header {
	stamp, day := at.UTC().Format(sigV4TimeFormat), at.UTC().Format(sigV4DateFormat)
	signed := [][2]string{{"content-type", "application/json"}, {"host", host}, {"x-amz-date", stamp}}
	if creds.SessionToken != "" {
		signed = append(signed, [2]string{"x-amz-security-token", creds.SessionToken})
	}
	var canonicalHeaders strings.Builder
	names := make([]string, 0, len(signed))
	for _, header := range signed {
		canonicalHeaders.WriteString(header[0] + ":" + header[1] + "\n")
		names = append(names, header[0])
	}
	signedHeaders := strings.Join(names, ";")
	canonicalRequest := strings.Join([]string{http.MethodPost, appSyncPublishPath, "", canonicalHeaders.String(), signedHeaders, hashSHA256(body)}, "\n")
	scope := strings.Join([]string{day, region, appSyncService, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{sigV4Algorithm, stamp, scope, hashSHA256([]byte(canonicalRequest))}, "\n")
	key := signHMAC([]byte("AWS4"+creds.SecretAccessKey), day)
	for _, part := range []string{region, appSyncService, "aws4_request"} {
		key = signHMAC(key, part)
	}
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("X-Amz-Date", stamp)
	if creds.SessionToken != "" {
		headers.Set("X-Amz-Security-Token", creds.SessionToken)
	}
	headers.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		sigV4Algorithm, creds.AccessKeyID, scope, signedHeaders, hex.EncodeToString(signHMAC(key, stringToSign))))
	return headers
}

func sendToAppSyncEvents(ctx context.Context, d *RealtimeDefinition, binding realtimeBinding, channel string, envelope []byte, _ string) error {
	failed := func(format string, args ...any) error {
		return fmt.Errorf("ocel: realtime %q: publish on %s: %s", d.name, channel, fmt.Sprintf(format, args...))
	}
	creds, err := readAWSCredentials(ctx)
	if err != nil {
		return failed("%v", err)
	}
	host := binding.properties.GetHost()
	body, err := json.Marshal(struct {
		Channel string   `json:"channel"`
		Events  []string `json:"events"`
	}{Channel: channel, Events: []string{string(envelope)}})
	if err != nil {
		return failed("%v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+appSyncPublishPath, bytes.NewReader(body))
	if err != nil {
		return failed("%v", err)
	}
	req.Header = signAppSyncPublish(host, body, creds, findAppSyncRegion(host), time.Now())
	res, err := realtimeHTTPClient.Do(req)
	if err != nil {
		return failed("%v", err)
	}
	defer res.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode/100 != 2 {
		return failed("AppSync refused it with status %d: %s", res.StatusCode, answer)
	}
	var published struct {
		Failed []json.RawMessage `json:"failed"`
	}
	if json.Unmarshal(answer, &published) == nil && len(published.Failed) > 0 {
		return failed("AppSync failed the event: %s", answer)
	}
	return nil
}
