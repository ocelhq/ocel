import datetime
import ipaddress
import json
import socket
import ssl
import threading

import pytest
import redis
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import NameOID

from ocel import kv


def _name(common: str) -> x509.Name:
    return x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, common)])


def _issue(subject: str, issuer=None, *, authority: bool, names=None):
    key = ec.generate_private_key(ec.SECP256R1())
    now = datetime.datetime.now(datetime.UTC)
    signer_name, signer_key = issuer if issuer else (_name(subject), key)
    builder = (
        x509.CertificateBuilder()
        .subject_name(_name(subject))
        .issuer_name(signer_name)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - datetime.timedelta(minutes=1))
        .not_valid_after(now + datetime.timedelta(hours=1))
        .add_extension(x509.BasicConstraints(ca=authority, path_length=None), critical=True)
        .add_extension(x509.SubjectKeyIdentifier.from_public_key(key.public_key()), critical=False)
        .add_extension(
            x509.AuthorityKeyIdentifier.from_issuer_public_key(signer_key.public_key()),
            critical=False,
        )
    )
    if authority:
        builder = builder.add_extension(
            x509.KeyUsage(
                digital_signature=True,
                content_commitment=False,
                key_encipherment=False,
                data_encipherment=False,
                key_agreement=False,
                key_cert_sign=True,
                crl_sign=True,
                encipher_only=False,
                decipher_only=False,
            ),
            critical=True,
        )
    else:
        builder = builder.add_extension(
            x509.SubjectAlternativeName(
                names or [x509.IPAddress(ipaddress.ip_address("127.0.0.1"))]
            ),
            critical=False,
        )
    certificate = builder.sign(signer_key, hashes.SHA256())
    return certificate, key


def _pem(certificate) -> str:
    return certificate.public_bytes(serialization.Encoding.PEM).decode()


_REPLIES = {b"HELLO": b"%1\r\n$5\r\nproto\r\n:3\r\n", b"PING": b"+PONG\r\n"}


class _Store:
    def __init__(self, tmp_path, names=None):
        self.authority, authority_key = _issue("store authority", authority=True)
        certificate, key = _issue(
            "store", (self.authority.subject, authority_key), authority=False, names=names
        )
        chain = tmp_path / "store.pem"
        chain.write_bytes(
            _pem(certificate).encode()
            + key.private_bytes(
                serialization.Encoding.PEM,
                serialization.PrivateFormat.PKCS8,
                serialization.NoEncryption(),
            )
        )
        self._context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        self._context.load_cert_chain(chain)
        self._listener = socket.create_server(("127.0.0.1", 0))
        self.port = self._listener.getsockname()[1]
        threading.Thread(target=self._serve, daemon=True).start()

    def _serve(self):
        while True:
            try:
                plain, _ = self._listener.accept()
            except OSError:
                return
            threading.Thread(target=self._answer, args=(plain,), daemon=True).start()

    def _answer(self, plain):
        try:
            with self._context.wrap_socket(plain, server_side=True) as connection:
                commands = connection.makefile("rb")
                while header := commands.readline():
                    arguments = []
                    for _ in range(int(header[1:])):
                        length = int(commands.readline()[1:])
                        arguments.append(commands.read(length + 2)[:-2])
                    connection.sendall(_REPLIES.get(arguments[0].upper(), b"+OK\r\n"))
        except (OSError, ssl.SSLError, ValueError):
            plain.close()

    def close(self):
        self._listener.close()


@pytest.fixture
def store(tmp_path):
    served = _Store(tmp_path)
    yield served
    served.close()


@pytest.fixture
def system_trust(tmp_path, monkeypatch):
    authority, _ = _issue("system authority", authority=True)
    trusted = tmp_path / "system.pem"
    trusted.write_text(_pem(authority))
    monkeypatch.setenv("SSL_CERT_FILE", str(trusted))
    monkeypatch.delenv("SSL_CERT_DIR", raising=False)
    return trusted


@pytest.fixture
def named_store(tmp_path):
    served = _Store(tmp_path, names=[x509.DNSName("cache.internal")])
    yield served
    served.close()


def _deliver(monkeypatch, name: str, port: int, ca_pem: str, **properties):
    monkeypatch.setenv(
        f"OCEL_RESOURCE_KV_{name}",
        json.dumps(
            {
                "name": f"kv--{name}",
                "kv": {
                    "host": "127.0.0.1",
                    "port": port,
                    "tls": True,
                    "caPem": ca_pem,
                    **properties,
                },
            }
        ),
    )


def test_a_forwarded_store_is_verified_under_the_tls_server_name_it_carries(
    monkeypatch, named_store
):
    _deliver(
        monkeypatch,
        "forwarded",
        named_store.port,
        _pem(named_store.authority),
        tlsServerName="cache.internal",
    )

    assert kv("forwarded").sync_client().ping() is True


@pytest.mark.asyncio
async def test_an_async_client_verifies_a_forwarded_store_under_its_tls_server_name(
    monkeypatch, named_store
):
    _deliver(
        monkeypatch,
        "forwarded",
        named_store.port,
        _pem(named_store.authority),
        tlsServerName="cache.internal",
    )
    client = kv("forwarded").client()

    assert await client.ping() is True
    await client.aclose()


def test_a_forwarded_store_whose_certificate_names_another_host_is_refused(
    monkeypatch, named_store
):
    _deliver(
        monkeypatch,
        "elsewhere",
        named_store.port,
        _pem(named_store.authority),
        tlsServerName="other.internal",
    )

    with pytest.raises(redis.ConnectionError, match="certificate verify failed"):
        kv("elsewhere").sync_client().ping()


def test_a_store_whose_certificate_chains_to_the_delivered_authority_is_reached(monkeypatch, store):
    _deliver(monkeypatch, "reached", store.port, _pem(store.authority))

    assert kv("reached").sync_client().ping() is True


@pytest.mark.asyncio
async def test_an_async_client_reaches_a_store_whose_certificate_chains_to_the_delivered_authority(
    monkeypatch, store
):
    _deliver(monkeypatch, "reached", store.port, _pem(store.authority))
    client = kv("reached").client()

    assert await client.ping() is True
    await client.aclose()


def test_a_store_only_the_system_trusts_is_refused(monkeypatch, store, system_trust):
    system_trust.write_text(_pem(store.authority))
    elsewhere, _ = _issue("elsewhere", authority=True)
    _deliver(monkeypatch, "refused", store.port, _pem(elsewhere))

    with pytest.raises(redis.ConnectionError, match="certificate verify failed"):
        kv("refused").sync_client().ping()


@pytest.mark.asyncio
async def test_an_async_client_refuses_a_store_only_the_system_trusts(
    monkeypatch, store, system_trust
):
    system_trust.write_text(_pem(store.authority))
    elsewhere, _ = _issue("elsewhere", authority=True)
    _deliver(monkeypatch, "refused", store.port, _pem(elsewhere))
    client = kv("refused").client()

    with pytest.raises(redis.ConnectionError, match="certificate verify failed"):
        await client.ping()
    await client.aclose()
