import json
import threading
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from ocel._aws_signature import (
    AwsCredentials,
    find_appsync_region,
    forget_container_credentials,
    read_aws_credentials,
    sign_appsync_publish,
)

BODY = b'{"channel":"/app/orders/o1","events":["{\\"v\\":1}"]}'
HOST = "abc123.appsync-api.eu-west-1.amazonaws.com"
AT = datetime(2026, 10, 3, 12, 0, 0, tzinfo=timezone.utc)
KEYS = AwsCredentials("AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY")


def test_a_publish_is_signed_as_the_aws_sdk_signs_it():
    assert sign_appsync_publish(HOST, BODY, KEYS, "eu-west-1", AT) == {
        "Content-Type": "application/json",
        "X-Amz-Date": "20261003T120000Z",
        "Authorization": "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261003/eu-west-1/appsync/"
        "aws4_request, SignedHeaders=content-type;host;x-amz-date, "
        "Signature=6c8f1bf8968ad3ec649127bc436717dd96a143637d076bfe6013567da6855ae1",
    }
    session = AwsCredentials(KEYS.access_key_id, KEYS.secret_access_key, "session-token")
    assert sign_appsync_publish(HOST, BODY, session, "eu-west-1", AT) == {
        "Content-Type": "application/json",
        "X-Amz-Date": "20261003T120000Z",
        "X-Amz-Security-Token": "session-token",
        "Authorization": "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261003/eu-west-1/appsync/"
        "aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-security-token, "
        "Signature=aa66eda43423cb89f230afcbf748369d3decaa2a23ef192d0b4a3d41087c747b",
    }


def test_the_region_is_read_from_the_apis_host_and_aws_region_for_any_other(monkeypatch):
    monkeypatch.setenv("AWS_REGION", "us-west-2")
    assert find_appsync_region(HOST) == "eu-west-1"
    assert find_appsync_region("realtime.example.com") == "us-west-2"


def test_the_environments_keys_are_read_first(monkeypatch):
    monkeypatch.setenv("AWS_ACCESS_KEY_ID", "AKIDENV")
    monkeypatch.setenv("AWS_SECRET_ACCESS_KEY", "secret")
    monkeypatch.setenv("AWS_SESSION_TOKEN", "token")
    assert read_aws_credentials() == AwsCredentials("AKIDENV", "secret", "token")


def test_a_containers_credentials_are_read_from_its_endpoint_once_while_fresh(monkeypatch):
    reads = []
    expires = (datetime.now(timezone.utc) + timedelta(hours=1)).isoformat()

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            reads.append(self.headers.get("Authorization"))
            body = json.dumps(
                {
                    "AccessKeyId": "ASIACONTAINER",
                    "SecretAccessKey": "container-secret",
                    "Token": "container-session",
                    "Expiration": expires,
                }
            ).encode()
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    host, port = server.server_address
    monkeypatch.delenv("AWS_ACCESS_KEY_ID", raising=False)
    monkeypatch.delenv("AWS_SECRET_ACCESS_KEY", raising=False)
    monkeypatch.delenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", raising=False)
    monkeypatch.setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", f"http://{host}:{port}/creds")
    monkeypatch.setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "container-token")
    forget_container_credentials()
    try:
        for _ in range(3):
            assert read_aws_credentials() == AwsCredentials(
                "ASIACONTAINER", "container-secret", "container-session"
            )
        assert reads == ["container-token"]
    finally:
        forget_container_credentials()
        server.shutdown()


def test_credentials_that_cannot_be_found_are_said_so(monkeypatch):
    for name in (
        "AWS_ACCESS_KEY_ID",
        "AWS_SECRET_ACCESS_KEY",
        "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
        "AWS_CONTAINER_CREDENTIALS_FULL_URI",
    ):
        monkeypatch.delenv(name, raising=False)
    forget_container_credentials()
    with pytest.raises(RuntimeError, match="AWS_ACCESS_KEY_ID"):
        read_aws_credentials()
