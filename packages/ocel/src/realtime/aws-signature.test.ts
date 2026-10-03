import { afterEach, describe, expect, it, vi } from "vitest";
import {
  findAppSyncRegion,
  forgetContainerCredentials,
  readAwsCredentials,
  signAppSyncPublish,
} from "./aws-signature.js";

const body = '{"channel":"/app/orders/o1","events":["{\\"v\\":1}"]}';
const keys = {
  accessKeyId: "AKIDEXAMPLE",
  secretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
};

describe("signAppSyncPublish", () => {
  it("signs a publish as the AWS SDK signs it", () => {
    const at = new Date(Date.UTC(2026, 9, 3, 12, 0, 0));
    const host = "abc123.appsync-api.eu-west-1.amazonaws.com";
    expect(signAppSyncPublish(host, body, keys, "eu-west-1", at)).toEqual({
      "content-type": "application/json",
      "x-amz-date": "20261003T120000Z",
      authorization:
        "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261003/eu-west-1/appsync/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=6c8f1bf8968ad3ec649127bc436717dd96a143637d076bfe6013567da6855ae1",
    });
    expect(
      signAppSyncPublish(host, body, { ...keys, sessionToken: "session-token" }, "eu-west-1", at),
    ).toEqual({
      "content-type": "application/json",
      "x-amz-date": "20261003T120000Z",
      "x-amz-security-token": "session-token",
      authorization:
        "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261003/eu-west-1/appsync/aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-security-token, Signature=aa66eda43423cb89f230afcbf748369d3decaa2a23ef192d0b4a3d41087c747b",
    });
  });
});

describe("findAppSyncRegion", () => {
  afterEach(() => vi.unstubAllEnvs());

  it("reads the region from the API's host, and AWS_REGION for any other host", () => {
    vi.stubEnv("AWS_REGION", "us-west-2");
    expect(findAppSyncRegion("abc123.appsync-api.eu-west-1.amazonaws.com")).toBe("eu-west-1");
    expect(findAppSyncRegion("realtime.example.com")).toBe("us-west-2");
  });
});

describe("readAwsCredentials", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
    forgetContainerCredentials();
  });

  it("reads the environment's keys first", async () => {
    vi.stubEnv("AWS_ACCESS_KEY_ID", "AKIDENV");
    vi.stubEnv("AWS_SECRET_ACCESS_KEY", "secret");
    vi.stubEnv("AWS_SESSION_TOKEN", "token");
    await expect(readAwsCredentials()).resolves.toEqual({
      accessKeyId: "AKIDENV",
      secretAccessKey: "secret",
      sessionToken: "token",
    });
  });

  it("reads a container's credentials from its endpoint once while they are fresh", async () => {
    vi.stubEnv("AWS_ACCESS_KEY_ID", "");
    vi.stubEnv("AWS_SECRET_ACCESS_KEY", "");
    vi.stubEnv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "/v2/credentials/abc");
    const fetched = vi.fn(async (_url: string) =>
      Response.json({
        AccessKeyId: "ASIACONTAINER",
        SecretAccessKey: "container-secret",
        Token: "container-session",
        Expiration: new Date(Date.now() + 3_600_000).toISOString(),
      }),
    );
    vi.stubGlobal("fetch", fetched);
    for (let i = 0; i < 3; i++) {
      await expect(readAwsCredentials()).resolves.toEqual({
        accessKeyId: "ASIACONTAINER",
        secretAccessKey: "container-secret",
        sessionToken: "container-session",
      });
    }
    expect(fetched).toHaveBeenCalledTimes(1);
    expect(fetched.mock.calls[0]?.[0]).toBe("http://169.254.170.2/v2/credentials/abc");
  });

  it("says which credentials it found none of", async () => {
    vi.stubEnv("AWS_ACCESS_KEY_ID", "");
    vi.stubEnv("AWS_SECRET_ACCESS_KEY", "");
    vi.stubEnv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "");
    vi.stubEnv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "");
    await expect(readAwsCredentials()).rejects.toThrow("AWS_ACCESS_KEY_ID");
  });

  function stubContainerEndpoint(endpoint: string) {
    vi.stubEnv("AWS_ACCESS_KEY_ID", "");
    vi.stubEnv("AWS_SECRET_ACCESS_KEY", "");
    vi.stubEnv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "");
    vi.stubEnv("AWS_CONTAINER_CREDENTIALS_FULL_URI", endpoint);
    vi.stubEnv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "container-token");
    forgetContainerCredentials();
  }

  it.each([
    "http://credentials.example.com/v2",
    "http://10.0.0.5/v2",
    "http://169.254.169.254/latest",
    "http://127.0.0.1.example.com/v2",
    "ftp://127.0.0.1/v2",
  ])("never sends the container's token to %s", async (endpoint) => {
    stubContainerEndpoint(endpoint);
    const fetched = vi.fn(async () => Response.json({}));
    vi.stubGlobal("fetch", fetched);
    await expect(readAwsCredentials()).rejects.toThrow("AWS_CONTAINER_CREDENTIALS_FULL_URI");
    expect(fetched).not.toHaveBeenCalled();
  });

  it.each([
    "http://localhost:9000/v2",
    "http://127.0.0.1/v2",
    "http://127.8.9.10/v2",
    "http://[::1]/v2",
    "http://169.254.170.2/v2",
    "http://169.254.170.23/v1",
    "http://[fd00:ec2::23]/v1",
    "https://credentials.example.com/v2",
  ])("asks %s for the container's credentials", async (endpoint) => {
    stubContainerEndpoint(endpoint);
    const fetched = vi.fn(async (_url: string) =>
      Response.json({ AccessKeyId: "ASIACONTAINER", SecretAccessKey: "container-secret" }),
    );
    vi.stubGlobal("fetch", fetched);
    await expect(readAwsCredentials()).resolves.toEqual({
      accessKeyId: "ASIACONTAINER",
      secretAccessKey: "container-secret",
    });
    expect(fetched.mock.calls[0]?.[0]).toBe(endpoint);
  });
});
