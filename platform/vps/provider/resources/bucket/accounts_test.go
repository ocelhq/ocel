package bucket

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestAnAccountIsNamedInWhatEveryStoreKeepsAnAccessKeyIn(t *testing.T) {
	t.Parallel()

	key := AccountKey("shop", "prod", "web")
	if len(key) != 20 {
		t.Errorf("AccountKey() = %q, %d characters, and a store keeps an access key in 3 to 20", key, len(key))
	}
	if key == AccountKey("shop", "prod", "admin") || key == AccountKey("shop", "preview-pr-1", "web") || key == AccountKey("blog", "prod", "web") {
		t.Error("two apps reach the store under one account, so either reaches what the other was granted")
	}
}

func TestTheSecretAnAppReachesTheStoreWithNeverRidesTheCommandLine(t *testing.T) {
	t.Parallel()

	const secret = "a-secret-no-process-list-should-show"
	account := Account{
		Store: "shop-prod-store-s3", Endpoint: "http://127.0.0.1:9000", Region: "us-east-1",
		RootKeyID: "ocel", RootSecret: "root-secret",
		AccessKeyID: AccountKey("shop", "prod", "web"), SecretKey: secret,
		Buckets: []string{"shop-prod-uploads"},
	}
	calls, err := account.calls()
	if err != nil {
		t.Fatalf("calls() = %v", err)
	}
	for _, call := range calls {
		script, err := accountScript(account, call, time.Unix(0, 0).UTC())
		if err != nil {
			t.Fatalf("accountScript(%s) = %v", call.what, err)
		}
		for what, form := range map[string]string{
			"in the clear": secret,
			"base64'd":     base64.StdEncoding.EncodeToString(call.body),
		} {
			if strings.Contains(script, form) {
				t.Errorf("the script that would %s contains the app's store secret %s, and every process on the box reads it out of the process list:\n%s",
					call.what, what, script)
			}
		}
	}
}
