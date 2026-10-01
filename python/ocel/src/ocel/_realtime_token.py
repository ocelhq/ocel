import base64
import json
import secrets
import time
from typing import Any, Literal

TokenOperation = Literal["connect", "subscribe", "publish"]


def _import_ed25519():
    try:
        from cryptography.hazmat.primitives.asymmetric import ed25519
    except ImportError:
        raise ImportError(
            "realtime tokens are signed with cryptography, which is not installed. "
            "Install it with the extra: pip install 'ocel[realtime]'"
        ) from None
    return ed25519


def _encode_base64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).decode().rstrip("=")


def _encode_canonical_json(value: Any) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


def sign_token(signing_key: bytes, header: dict[str, Any], claims: dict[str, Any]) -> str:
    key = _import_ed25519().Ed25519PrivateKey.from_private_bytes(signing_key)
    encoded_header = _encode_base64url(_encode_canonical_json(header))
    encoded_claims = _encode_base64url(_encode_canonical_json(claims))
    signing_input = f"{encoded_header}.{encoded_claims}"
    return f"{signing_input}.{_encode_base64url(key.sign(signing_input.encode()))}"


def mint_token(
    signing_key: bytes,
    *,
    namespace: str,
    audience: str,
    subject: str,
    operation: TokenOperation,
    channel: str,
    ttl_seconds: int,
) -> dict[str, Any]:
    issued_at = int(time.time())
    expires_at = issued_at + ttl_seconds
    token = sign_token(
        signing_key,
        {"alg": "EdDSA", "typ": "JWT"},
        {
            "iss": f"ocel:rt:{namespace}",
            "aud": audience,
            "sub": subject,
            "iat": issued_at,
            "exp": expires_at,
            "jti": _encode_base64url(secrets.token_bytes(16)),
            "ocel": {"op": operation, "ch": channel, "ns": namespace},
        },
    )
    return {"token": token, "expiresAt": expires_at}
