package tracing

import (
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

func TestACredentialHeaderIsRedactedAndAnOrdinaryOneIsKept(t *testing.T) {
	for _, tc := range []struct{ name, value, want string }{
		{"Authorization", "Bearer abc", Redacted},
		{"Proxy-Authorization", "Basic abc", Redacted},
		{"Cookie", "session=abc", Redacted},
		{"Set-Cookie", "session=abc; Path=/", Redacted},
		{"X-Amz-Security-Token", "FwoGZX", Redacted},
		{"X-Goog-Iam-Authorization-Token", "ya29", Redacted},
		{"X-Api-Key", "k", Redacted},
		{"Content-Type", "application/json", "application/json"},
		{"X-Amz-Date", "20261027T000000Z", "20261027T000000Z"},
	} {
		if got := redactHeader(tc.name, tc.value); got != tc.want {
			t.Errorf("redactHeader(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestASignedURLKeepsItsPathAndPlainParametersAndLosesItsSignature(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{
			"https://b.s3.amazonaws.com/k?X-Amz-Credential=AKID%2F2026&X-Amz-Signature=dead&partNumber=2",
			"https://b.s3.amazonaws.com/k?X-Amz-Credential=" + Redacted + "&X-Amz-Signature=" + Redacted + "&partNumber=2",
		},
		{"https://storage.googleapis.com/o?X-Goog-Signature=beef", "https://storage.googleapis.com/o?X-Goog-Signature=" + Redacted},
		{"https://x.blob.core.windows.net/c?sig=abc&sv=2024", "https://x.blob.core.windows.net/c?sig=" + Redacted + "&sv=2024"},
		{"https://api.example.com/v1?access_token=t&page=2", "https://api.example.com/v1?access_token=" + Redacted + "&page=2"},
		{"postgres://ocel:hunter2@db:5432/app", "postgres://ocel:" + Redacted + "@db:5432/app"},
		{"https://example.com/plain", "https://example.com/plain"},
	} {
		if got := redactURL(tc.in); got != tc.want {
			t.Errorf("redactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAnEnvValueIsKeptOnlyWhenItsVariableIsKnownNotToBeSecret(t *testing.T) {
	for _, tc := range []struct{ name, value, want string }{
		{"AWS_SECRET_ACCESS_KEY", "s", Redacted},
		{"GITHUB_TOKEN", "ghp", Redacted},
		{"PGPASSWORD", "p", Redacted},
		{"DB_PASSWD", "p", Redacted},
		{"GOOGLE_APPLICATION_CREDENTIALS", "/k.json", Redacted},
		{"SSH_PRIVATE", "k", Redacted},
		{"AWS_SESSION_TOKEN", "t", Redacted},
		{"BASIC_AUTH", "u:p", Redacted},
		{"DATABASE_URL", "postgres://db/app", Redacted},
		{"STRIPE_WEBHOOK", "whsec_live", Redacted},
		{"PATH", "/usr/bin", "/usr/bin"},
		{"NODE_ENV", "production", "production"},
	} {
		if got := redactEnv(tc.name, tc.value); got != tc.want {
			t.Errorf("redactEnv(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestUnderAFullTraceAnEnvValueIsKeptUnlessItsNameOrItsTextSaysItIsSecret(t *testing.T) {
	t.Setenv(ModeEnvVar, string(ModeFull))
	for _, tc := range []struct{ name, value, want string }{
		{"AWS_SECRET_ACCESS_KEY", "s", Redacted},
		{"GITHUB_TOKEN", "ghp", Redacted},
		{"PGPASSWORD", "p", Redacted},
		{"DATABASE_URL", "postgres://ocel:hunter2@db/app", "postgres://ocel:" + Redacted + "@db/app"},
		{"DATABASE_URL", "postgres://db/app", "postgres://db/app"},
		{"STRIPE_WEBHOOK", "whsec_live", "whsec_live"},
		{"PATH", "/usr/bin", "/usr/bin"},
	} {
		if got := redactEnv(tc.name, tc.value); got != tc.want {
			t.Errorf("redactEnv(%q, %q) = %q, want %q", tc.name, tc.value, got, tc.want)
		}
	}
}

func TestFreeTextLosesCredentialsItCarriesAndKeepsTheRest(t *testing.T) {
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----"
	for _, tc := range []struct{ in, want string }{
		{"key is " + pem + " ok", "key is " + Redacted + " ok"},
		{"Authorization: Bearer eyJhbGci.x.y", "Authorization: " + Redacted},
		{"curl -H 'authorization: token abc'", "curl -H 'authorization: " + Redacted + "'"},
		{"env AWS_SECRET_ACCESS_KEY=abc AWS_REGION=eu", "env AWS_SECRET_ACCESS_KEY=" + Redacted + " AWS_REGION=eu"},
		{"dial postgres://u:pw@h/db failed", "dial postgres://u:" + Redacted + "@h/db failed"},
		{"GET https://h/o?X-Amz-Signature=dead&a=1", "GET https://h/o?X-Amz-Signature=" + Redacted + "&a=1"},
		{
			`{"4dabf18193072939515e22adb298388d":"1b47061264138c4ac30d75fd1eb44270","plaintext":"\"hunter2\""}`,
			`{"4dabf18193072939515e22adb298388d":"1b47061264138c4ac30d75fd1eb44270","plaintext":"` + Redacted + `"}`,
		},
		{
			`{"4dabf18193072939515e22adb298388d":"1b47061264138c4ac30d75fd1eb44270","ciphertext":"v1:abc"}`,
			`{"4dabf18193072939515e22adb298388d":"1b47061264138c4ac30d75fd1eb44270","ciphertext":"` + Redacted + `"}`,
		},
		{"deploying web to eu-west-1", "deploying web to eu-west-1"},
	} {
		if got := RedactText(tc.in); got != tc.want {
			t.Errorf("RedactText(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

func TestAttributesAreRedactedByWhatTheirKeySaysTheyHold(t *testing.T) {
	got := new(SecretValues).RedactAttributes([]attribute.KeyValue{
		attribute.String("http.request.header.authorization", "Bearer abc"),
		attribute.StringSlice("http.response.header.set-cookie", []string{"a=1", "b=2"}),
		attribute.String("url.full", "https://h/o?X-Amz-Signature=dead"),
		attribute.String(envAttributePrefix+"GITHUB_TOKEN", "ghp_x"),
		attribute.String(envAttributePrefix+"NODE_ENV", "production"),
		attribute.StringSlice("process.command_args", []string{"psql", "postgres://u:pw@h/db"}),
		attribute.String("http.response.body", `{"token_type":"Bearer","note":"Authorization: Bearer z"}`),
		attribute.Int("http.response.status_code", 200),
	})
	want := map[string]string{
		"http.request.header.authorization": Redacted,
		"http.response.header.set-cookie":   `["` + Redacted + `","` + Redacted + `"]`,
		"url.full":                          "https://h/o?X-Amz-Signature=" + Redacted,
		envAttributePrefix + "GITHUB_TOKEN": Redacted,
		envAttributePrefix + "NODE_ENV":     "production",
		"process.command_args":              `["psql","postgres://u:` + Redacted + `@h/db"]`,
		"http.response.status_code":         "200",
	}
	for _, keyValue := range got {
		key := string(keyValue.Key)
		if key == "http.response.body" {
			if strings.Contains(keyValue.Value.AsString(), "Bearer z") {
				t.Errorf("body = %q, want the header in it redacted", keyValue.Value.AsString())
			}
			continue
		}
		if keyValue.Value.String() != want[key] {
			t.Errorf("%s = %q, want %q", key, keyValue.Value.String(), want[key])
		}
	}
	if len(got) != 8 {
		t.Errorf("kept %d attributes, want all 8", len(got))
	}
}

func TestAnArrayOrMapAttributeIsRedactedLikeTheStringsItHolds(t *testing.T) {
	got := new(SecretValues).RedactAttributes([]attribute.KeyValue{
		attribute.Slice("http.request.header.authorization", attribute.StringValue("Bearer ya29.CHILD")),
		attribute.Slice("process.command_args", attribute.StringValue("tool"), attribute.StringValue("--password"), attribute.StringValue("hunter2")),
		attribute.Slice(envAttributePrefix+"GITHUB_TOKEN", attribute.StringValue("ghp_x")),
		attribute.Slice("notes", attribute.Int64Value(7), attribute.StringValue("dial postgres://u:pw@h/db")),
		attribute.Map("http.request.header", attribute.String("x-api-key", "cfkey"), attribute.String("accept", "*/*")),
		attribute.ByteSlice("http.response.body", []byte("Authorization: Bearer z")),
	})
	want := map[string]string{
		"http.request.header.authorization": `["` + Redacted + `"]`,
		"process.command_args":              `["tool","--password","` + Redacted + `"]`,
		envAttributePrefix + "GITHUB_TOKEN": `["` + Redacted + `"]`,
		"notes":                             `[7,"dial postgres://u:` + Redacted + `@h/db"]`,
		"http.request.header":               `{"accept":"*/*","x-api-key":"` + Redacted + `"}`,
		"http.response.body":                "Authorization: " + Redacted,
	}
	for _, keyValue := range got {
		value := keyValue.Value.String()
		if keyValue.Value.Type() == attribute.BYTESLICE {
			value = string(keyValue.Value.AsByteSlice())
		}
		if value != want[string(keyValue.Key)] {
			t.Errorf("%s = %s, want %s", keyValue.Key, value, want[string(keyValue.Key)])
		}
	}
}

func TestASecretNamedJSONValueIsRedactedAndItsNeighboursKept(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			"an Infisical secret",
			`{"secrets":[{"secretKey":"DB_URL","secretValue":"postgres://u:p@h/db","type":"shared"}]}`,
			`{"secrets":"` + Redacted + `"}`,
		},
		{
			"an Infisical secret under a plain key",
			`{"items":[{"secretKey":"DB_URL","secretValue":"postgres://u:p@h/db","type":"shared"}]}`,
			`{"items":[{"secretKey":"` + Redacted + `","secretValue":"` + Redacted + `","type":"shared"}]}`,
		},
		{
			"an OAuth token response",
			`{"access_token":"ya29.a0","expires_in":3599,"refresh_token":"1//0g","scope":"openid"}`,
			`{"access_token":"` + Redacted + `","expires_in":3599,"refresh_token":"` + Redacted + `","scope":"openid"}`,
		},
		{
			"a password with escaped quotes",
			`{"user": "ada", "password" : "a\"b\\c"}`,
			`{"user": "ada", "password" : "` + Redacted + `"}`,
		},
		{
			"a secret-named object",
			`{"credentials":{"user":"ada","pin":"1234"},"region":"eu"}`,
			`{"credentials":"` + Redacted + `","region":"eu"}`,
		},
		{
			"a head cut inside the secret",
			`{"name":"web","client_secret":"GOCSPX-abcd`,
			`{"name":"web","client_secret":"` + Redacted + `"`,
		},
		{
			"a head cut inside a secret-named object",
			`{"name":"web","session":{"id":"s1","nonce":"n`,
			`{"name":"web","session":"` + Redacted + `"`,
		},
		{
			"JSON quoted inside a log line",
			`stdout: "{\"password\":\"hunter2\",\"user\":\"ada\"}"`,
			`stdout: "{\"password\":\"` + Redacted + `\",\"user\":\"ada\"}"`,
		},
		{
			"JSON quoted inside a log line with an escaped quote in the secret",
			`stdout: "{\"password\":\"a\\\"b\",\"user\":\"ada\"}"`,
			`stdout: "{\"password\":\"` + Redacted + `\",\"user\":\"ada\"}"`,
		},
	} {
		if got := RedactText(tc.in); got != tc.want {
			t.Errorf("%s: RedactText(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestASecretNamedJSONValueIsRedactedWhateverItsType(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"a number", `{"password":123456,"user":"ada"}`, `{"password":"` + Redacted + `","user":"ada"}`},
		{"a negative exponent number", `{"api_key": -12.5e+3 ,"port":8080}`, `{"api_key": "` + Redacted + `","port":8080}`},
		{"a boolean", `{"token":true,"verbose":false}`, `{"token":"` + Redacted + `","verbose":false}`},
		{"null", `{"session":null}`, `{"session":"` + Redacted + `"}`},
		{"the last value of an array", `{"items":[{"pin":1,"password":42}],"count":1}`, `{"items":[{"pin":1,"password":"` + Redacted + `"}],"count":1}`},
		{"a number on its own line", "{\n  \"password\": 123456\n}", "{\n  \"password\": \"" + Redacted + "\"\n}"},
		{"an unquoted word", `{"password":hunter2}`, `{"password":"` + Redacted + `"}`},
		{"a head cut inside the number", `{"name":"web","password":12345`, `{"name":"web","password":"` + Redacted + `"`},
		{
			"a number in JSON quoted inside a log line",
			`stdout: "{\"password\":123456,\"user\":\"ada\"}"`,
			`stdout: "{\"password\":\"` + Redacted + `\",\"user\":\"ada\"}"`,
		},
		{
			"a number closing JSON quoted inside a log line",
			`stdout: "{\"user\":\"ada\",\"password\":123456}"`,
			`stdout: "{\"user\":\"ada\",\"password\":\"` + Redacted + `\"}"`,
		},
	} {
		if got := RedactText(tc.in); got != tc.want {
			t.Errorf("%s: RedactText(%q)\n got %q\nwant %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestAURLFragmentLosesEveryValueItNamesAndKeepsAPlainAnchor(t *testing.T) {
	const token = "q3Z-v_8kR2mNpX4sT7uW9yA1bC5dE6fG0hJ2kL3mN4o"
	for _, tc := range []struct{ in, want string }{
		{"http://127.0.0.1:43123/#t=" + token, "http://127.0.0.1:43123/#t=" + Redacted},
		{"https://app.example/cb#access_token=abc&state=xyz", "https://app.example/cb#access_token=" + Redacted + "&state=" + Redacted},
		{"https://ocel.dev/docs#targets", "https://ocel.dev/docs#targets"},
	} {
		if got := RedactText(tc.in); got != tc.want {
			t.Errorf("RedactText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	got := new(SecretValues).RedactAttributes([]attribute.KeyValue{CommandArgsKey.StringSlice([]string{"wslview", "http://127.0.0.1:43123/#t=" + token})})
	if args := got[0].Value.String(); strings.Contains(args, token) {
		t.Errorf("args = %s, want the editor's token redacted", args)
	}
}

func TestTheValueAfterASecretNamedFlagIsRedactedInACommandsArguments(t *testing.T) {
	got := new(SecretValues).RedactAttributes([]attribute.KeyValue{attribute.StringSlice("process.command_args",
		[]string{"infisical", "export", "--api-token", "st.abc", "--password=hunter2", "--env", "prod", "--token", "--silent"})})
	want := `["infisical","export","--api-token","` + Redacted + `","--password=` + Redacted + `","--env","prod","--token","--silent"]`
	if got[0].Value.String() != want {
		t.Errorf("args = %s, want %s", got[0].Value.String(), want)
	}
	if text := RedactText("ran infisical --password=hunter2 --env prod"); strings.Contains(text, "hunter2") {
		t.Errorf("RedactText = %q, want the flag's value redacted", text)
	}
}

func TestTheValueAfterASecretNamedFlagIsRedactedInAShellCommandString(t *testing.T) {
	got := new(SecretValues).RedactAttributes([]attribute.KeyValue{CommandArgsKey.StringSlice(
		[]string{"sh", "-c", `tool --password hunter2 --api-token "st abc" --db-secret 'x y' --env prod --token --silent`})})
	want := `["sh","-c","tool --password ` + Redacted + ` --api-token ` + Redacted + ` --db-secret ` + Redacted + ` --env prod --token --silent"]`
	if got[0].Value.String() != want {
		t.Errorf("args = %s, want %s", got[0].Value.String(), want)
	}
}

func TestAFormEncodedBodyLosesItsSecretsAndKeepsItsPlainFields(t *testing.T) {
	in := "grant_type=authorization_code&code=4/0AeaY&code_verifier=dBjftJeZ&client_id=web&client_secret=GOCSPX&refresh_token=1//0g"
	want := "grant_type=authorization_code&code=" + Redacted + "&code_verifier=" + Redacted + "&client_id=web&client_secret=" + Redacted + "&refresh_token=" + Redacted
	if got := RedactText(in); got != want {
		t.Errorf("RedactText(%q) = %q, want %q", in, got, want)
	}
}

func TestACredentialAnyEndpointReturnsOrAcceptsIsRedactedWhereverItAppears(t *testing.T) {
	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ3ZWIifQ.c2ln-_"
	for _, tc := range []struct{ name, in, want string }{
		{"a JWT-bearer assertion", "grant_type=jwt-bearer&assertion=abc.def&scope=x", "grant_type=jwt-bearer&assertion=" + Redacted + "&scope=x"},
		{"a client assertion", "client_assertion=abc.def&client_id=web", "client_assertion=" + Redacted + "&client_id=web"},
		{"a signed JWT", `{"name":"web","signedJwt":"abc"}`, `{"name":"web","signedJwt":"` + Redacted + `"}`},
		{"a signed blob", `{"name":"web","signedBlob":"abc"}`, `{"name":"web","signedBlob":"` + Redacted + `"}`},
		{"a camelCase code verifier", `{"clientId":"c1","codeVerifier":"abc"}`, `{"clientId":"c1","codeVerifier":"` + Redacted + `"}`},
		{
			"XML credentials",
			"<Credentials><AccessKeyId>ASIA1</AccessKeyId><SecretAccessKey>abc</SecretAccessKey><SessionToken>def</SessionToken><Expiration>2026</Expiration></Credentials>",
			"<Credentials>" + Redacted + "</Credentials>",
		},
		{
			"an XML secret under a plain element",
			"<Result><SecretAccessKey>abc</SecretAccessKey><Expiration>2026</Expiration></Result>",
			"<Result><SecretAccessKey>" + Redacted + "</SecretAccessKey><Expiration>2026</Expiration></Result>",
		},
		{"a raw JWT", "signed in with " + jwt + " until noon", "signed in with " + Redacted + " until noon"},
		{"an unsigned JWT", "token " + "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ3ZWIifQ.", "token " + Redacted},
		{"a bare AWS secret key", "export wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY now", "export " + Redacted + " now"},
		{"a commit SHA", "deployed 9fceb02d0ae598e95dc970b74767f19372d61af8", "deployed 9fceb02d0ae598e95dc970b74767f19372d61af8"},
		{"a source path", "platform/vps/provider/boxstore/cipher.go", "platform/vps/provider/boxstore/cipher.go"},
	} {
		if got := RedactText(tc.in); got != tc.want {
			t.Errorf("%s: RedactText(%q)\n got %q\nwant %q", tc.name, tc.in, got, tc.want)
		}
	}
}
