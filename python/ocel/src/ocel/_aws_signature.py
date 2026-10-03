import hashlib
import hmac
import json
import os
import re
import threading
import time
import urllib.request
from dataclasses import dataclass
from datetime import datetime, timezone

_CONTAINER_CREDENTIALS_ORIGIN = "http://169.254.170.2"
_REFRESH_BEFORE_EXPIRY_SECONDS = 300
_CREDENTIALS_TIMEOUT_SECONDS = 5
_APPSYNC_REGION = re.compile(r"\.appsync-api\.([a-z0-9-]+)\.")


@dataclass(frozen=True)
class AwsCredentials:
    access_key_id: str
    secret_access_key: str
    session_token: str = ""


_held_lock = threading.Lock()
_held: tuple[AwsCredentials, float] | None = None


def forget_container_credentials() -> None:
    global _held
    with _held_lock:
        _held = None


def _read_container_credentials(endpoint: str) -> AwsCredentials:
    global _held
    with _held_lock:
        if _held is not None and _held[1] - time.time() > _REFRESH_BEFORE_EXPIRY_SECONDS:
            return _held[0]
        request = urllib.request.Request(endpoint)
        token_file = os.environ.get("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE")
        if token_file:
            with open(token_file) as file:
                token = file.read().strip()
        else:
            token = os.environ.get("AWS_CONTAINER_AUTHORIZATION_TOKEN", "")
        if token:
            request.add_header("Authorization", token)
        with urllib.request.urlopen(request, timeout=_CREDENTIALS_TIMEOUT_SECONDS) as response:
            answered = json.loads(response.read())
        if not answered.get("AccessKeyId") or not answered.get("SecretAccessKey"):
            raise RuntimeError("the container's credentials endpoint answered no AWS credentials")
        credentials = AwsCredentials(
            answered["AccessKeyId"], answered["SecretAccessKey"], answered.get("Token", "")
        )
        expiration = answered.get("Expiration")
        expires_at = (
            datetime.fromisoformat(expiration.replace("Z", "+00:00")).timestamp()
            if expiration
            else 0.0
        )
        _held = (credentials, expires_at)
        return credentials


def read_aws_credentials() -> AwsCredentials:
    access_key_id = os.environ.get("AWS_ACCESS_KEY_ID")
    secret_access_key = os.environ.get("AWS_SECRET_ACCESS_KEY")
    if access_key_id and secret_access_key:
        return AwsCredentials(
            access_key_id, secret_access_key, os.environ.get("AWS_SESSION_TOKEN", "")
        )
    relative = os.environ.get("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI")
    endpoint = (
        _CONTAINER_CREDENTIALS_ORIGIN + relative
        if relative
        else os.environ.get("AWS_CONTAINER_CREDENTIALS_FULL_URI")
    )
    if not endpoint:
        raise RuntimeError(
            "no AWS credentials to sign an AppSync publish with: set AWS_ACCESS_KEY_ID and "
            "AWS_SECRET_ACCESS_KEY, or run where AWS_CONTAINER_CREDENTIALS_RELATIVE_URI is "
            "delivered"
        )
    return _read_container_credentials(endpoint)


def find_appsync_region(host: str) -> str:
    match = _APPSYNC_REGION.search(host)
    return match.group(1) if match else os.environ.get("AWS_REGION", "")


def _hash(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _sign(key: bytes, data: str) -> bytes:
    return hmac.new(key, data.encode(), hashlib.sha256).digest()


def sign_appsync_publish(
    host: str, body: bytes, credentials: AwsCredentials, region: str, at: datetime
) -> dict[str, str]:
    stamp = at.astimezone(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    day = stamp[:8]
    signed = [("content-type", "application/json"), ("host", host), ("x-amz-date", stamp)]
    if credentials.session_token:
        signed.append(("x-amz-security-token", credentials.session_token))
    signed_headers = ";".join(name for name, _ in signed)
    canonical_request = "\n".join(
        [
            "POST",
            "/event",
            "",
            "".join(f"{name}:{value}\n" for name, value in signed),
            signed_headers,
            _hash(body),
        ]
    )
    scope = f"{day}/{region}/appsync/aws4_request"
    string_to_sign = "\n".join(
        ["AWS4-HMAC-SHA256", stamp, scope, _hash(canonical_request.encode())]
    )
    key = _sign(f"AWS4{credentials.secret_access_key}".encode(), day)
    for part in (region, "appsync", "aws4_request"):
        key = _sign(key, part)
    signature = hmac.new(key, string_to_sign.encode(), hashlib.sha256).hexdigest()
    headers = {"Content-Type": "application/json", "X-Amz-Date": stamp}
    if credentials.session_token:
        headers["X-Amz-Security-Token"] = credentials.session_token
    headers["Authorization"] = (
        f"AWS4-HMAC-SHA256 Credential={credentials.access_key_id}/{scope}, "
        f"SignedHeaders={signed_headers}, Signature={signature}"
    )
    return headers
