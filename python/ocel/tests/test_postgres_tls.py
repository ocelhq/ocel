import json
import ssl
from urllib.parse import parse_qs, urlparse

import pytest

from ocel import postgres

_CA = """-----BEGIN CERTIFICATE-----
MIIBejCCASCgAwIBAgIBATAKBggqhkjOPQQDAjASMRAwDgYDVQQDEwd0ZXN0IGNh
MB4XDTI0MDEwMTAwMDAwMFoXDTM0MDEwMTAwMDAwMFowEjEQMA4GA1UEAxMHdGVz
dCBjYTBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABBdFNY1ha8i9ZmwCaLaB9R0s
6D7Ls4wmH5o3pS2Eu3wD1qk4oA3N7dc/M4TkcNZ/T6xOr9Smn2Tlp8IB0ftB0m6j
YTBfMA4GA1UdDwEB/wQEAwICpDAdBgNVHSUEFjAUBggrBgEFBQcDAQYIKwYBBQUH
AwIwDwYDVR0TAQH/BAUwAwEB/zAdBgNVHQ4EFgQUAAAAAAAAAAAAAAAAAAAAAAAA
AAAwCgYIKoZIzj0EAwIDSAAwRQIhAP7fQZpg2XbJa7Y7c6k9l2d2uRk0Q8ZfA5N1
T+YxGq5XAiBcU7Jq2K1O7Hq2+z7E1l0p8vYv3f0Z2mE1Jd9b3GvOqw==
-----END CERTIFICATE-----
"""


def _record(monkeypatch, properties):
    monkeypatch.delenv("OCEL_PHASE", raising=False)
    monkeypatch.setenv(
        "OCEL_RESOURCE_POSTGRES_main", json.dumps({"name": "main", "postgres": properties})
    )


def test_a_records_url_is_the_connection_verbatim(monkeypatch):
    url = (
        "postgres://app:s3cret@ep-cool.neon.tech/orders?sslmode=require&options=endpoint%3Dep-cool"
    )
    _record(monkeypatch, {"url": url})

    assert postgres("main").connection_string == url


def test_a_records_tls_mode_is_the_connection_strings_sslmode(monkeypatch):
    _record(
        monkeypatch,
        {
            "host": "db",
            "port": 5432,
            "database": "d",
            "username": "u",
            "password": "p",
            "tlsMode": "POSTGRES_TLS_MODE_REQUIRE",
        },
    )

    query = parse_qs(urlparse(postgres("main").connection_string).query)
    assert query["sslmode"] == ["require"]


@pytest.mark.asyncio
async def test_a_record_under_verify_full_trusts_its_own_ca(monkeypatch):
    _record(
        monkeypatch,
        {
            "host": "db.example.com",
            "port": 5432,
            "database": "d",
            "username": "u",
            "password": "p",
            "tlsMode": "POSTGRES_TLS_MODE_VERIFY_FULL",
            "tlsCa": _CA,
        },
    )
    import asyncpg

    opened = []

    async def create_pool(dsn, **options):
        opened.append((dsn, options))
        return object()

    monkeypatch.setattr(asyncpg, "create_pool", create_pool)
    loaded = []
    monkeypatch.setattr(
        ssl.SSLContext, "load_verify_locations", lambda self, **kwargs: loaded.append(kwargs)
    )

    await postgres("main").pool()

    ((dsn, options),) = opened
    assert parse_qs(urlparse(dsn).query)["sslmode"] == ["verify-full"]
    context = options["ssl"]
    assert context.check_hostname is True
    assert context.verify_mode == ssl.CERT_REQUIRED
    assert loaded == [{"cadata": _CA}]


@pytest.mark.asyncio
async def test_a_record_naming_no_ca_leaves_tls_to_the_connection_string(monkeypatch):
    _record(
        monkeypatch,
        {
            "host": "db",
            "port": 5432,
            "database": "d",
            "username": "u",
            "password": "p",
            "tlsMode": "POSTGRES_TLS_MODE_REQUIRE",
        },
    )
    import asyncpg

    opened = []

    async def create_pool(dsn, **options):
        opened.append(options)
        return object()

    monkeypatch.setattr(asyncpg, "create_pool", create_pool)

    await postgres("main").pool()

    assert opened == [{}]


@pytest.mark.asyncio
async def test_a_ca_under_require_leaves_the_certificate_unchecked(monkeypatch):
    _record(
        monkeypatch,
        {
            "host": "db",
            "port": 5432,
            "database": "d",
            "username": "u",
            "password": "p",
            "tlsMode": "POSTGRES_TLS_MODE_REQUIRE",
            "tlsCa": _CA,
        },
    )
    import asyncpg

    opened = []

    async def create_pool(dsn, **options):
        opened.append((dsn, options))
        return object()

    monkeypatch.setattr(asyncpg, "create_pool", create_pool)

    await postgres("main").pool()

    ((dsn, options),) = opened
    assert parse_qs(urlparse(dsn).query)["sslmode"] == ["require"]
    assert options == {}
