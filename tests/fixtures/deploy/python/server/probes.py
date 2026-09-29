import gzip
import hashlib
import json
import os
import re
import time
import zlib
from email.parser import BytesParser
from email.policy import HTTP
from urllib.parse import parse_qs, unquote

import six

MAX_SLEEP_MS = 60_000

MAX_BODY = 8 << 20

STREAM_CHUNKS = 5

STREAM_GAP = 0.2

STREAM_END = b"ocel-stream-end\n"

CHUNK_BYTES = 64 * 1024

MAX_COOKIES = 10

MAX_EVENTS = 10

MAX_GAP_MS = 60_000

COMPRESSIBLE = {"marker": "ocel-compress", "filler": "ocel " * 1024}

PROBE_HEADER = "x-ocel-probe"

CHECKSUM_FIELD = "x-ocel-sha256"

ECHO_PREFIX = "/api/probes/echo/"

STATUS_PATH = re.compile(r"^/api/probes/status/(\d+)$")

EMPTY_PATH = re.compile(r"^/api/probes/empty/([^/]+)$")

GZIP_TOKEN = re.compile(r"\bgzip\b")

CORS_HEADERS = [
    ("access-control-allow-origin", "*"),
    ("access-control-allow-methods", "GET, POST, OPTIONS"),
    ("access-control-allow-headers", "content-type, x-ocel-probe"),
]


def stream(req):
    req.start_chunks(
        [
            ("content-type", "text/plain; charset=utf-8"),
            ("cache-control", "no-store, no-transform"),
        ]
    )
    for i in range(1, STREAM_CHUNKS):
        req.write_chunk(f"ocel-stream-{i}\n".encode())
        time.sleep(STREAM_GAP)
    req.write_chunk(STREAM_END)
    req.end_chunks()


def sse(req):
    events = bounded(req, "events", 3, MAX_EVENTS)
    gap = bounded(req, "gap", 1000, MAX_GAP_MS)
    if events is None or gap is None:
        req.write_json(400, {"error": f"events must be 0..{MAX_EVENTS} and gap 0..{MAX_GAP_MS}"})
        return
    req.start_chunks(
        [("content-type", "text/event-stream"), ("cache-control", "no-store, no-transform")]
    )
    for i in range(1, events + 1):
        req.write_chunk(f"id: {i}\ndata: ocel-sse-{i} {time.time_ns() // 1_000_000}\n\n".encode())
        if i < events:
            time.sleep(gap / 1000)
    req.write_chunk(b"event: end\ndata: ocel-sse-end\n\n")
    req.end_chunks()


def status(req):
    code = int(STATUS_PATH.match(path_of(req)).group(1))
    if code == 204:
        req.write_status(204)
        return
    extra = [("location", "/api/probes/status/204")] if 300 <= code < 400 else []
    req.write_json(code, {"status": code}, extra)


def empty(req):
    kind = EMPTY_PATH.match(path_of(req)).group(1)
    if kind == "redirect":
        req.write_status(302, [("location", "/api/probes/status/204"), ("content-length", "0")])
        return
    if kind == "ok":
        req.write_status(200, [("content-length", "0")])
        return
    req.write_json(404, {"error": f"no empty probe called {kind}"})


def echo(req):
    read = req.read_body()
    if read is None:
        refuse(req)
        return
    path = path_of(req)
    search = req.path[len(path) :]
    rest = path[len(ECHO_PREFIX) :] if path.startswith(ECHO_PREFIX) else ""
    req.write_json(
        200,
        {
            "method": req.command,
            "path": path,
            "search": search,
            "segments": [unquote(segment) for segment in rest.split("/")] if rest else [],
            "query": {name: values[0] for name, values in query_of(req).items()},
            "header": req.headers.get(PROBE_HEADER),
            "body": echoed(req, read),
        },
    )


def echoed(req, read):
    if not read:
        return None
    if "application/json" in (req.headers.get("content-type") or ""):
        try:
            return json.loads(read)
        except ValueError:
            pass
    return read.decode("utf-8", "replace")


def headers(req):
    seen = {}
    for name, value in req.headers.items():
        key = name.lower()
        seen[key] = f"{seen[key]}, {value}" if key in seen else value
    remote = req.client_address[0]
    req.write_json(
        200,
        {
            "headers": seen,
            "remote": remote,
            "ip": remote,
            "protocol": "http",
            "hostname": hostname_of(req.headers.get("host") or ""),
        },
    )


def hostname_of(host):
    if host.startswith("["):
        return host[: host.find("]") + 1]
    return host.split(":", 1)[0]


def auth(req):
    req.write_json(
        401,
        {"error": "unauthorized"},
        [("www-authenticate", 'Bearer realm="ocel"'), ("x-ocel-number", "42")],
    )


def method(req):
    if req.command not in ("GET", "HEAD"):
        req.write_json(405, {"error": f"{req.command} is not allowed"}, [("allow", "GET, HEAD")])
        return
    req.write_json(200, {"method": req.command})


def cors(req):
    if req.command == "OPTIONS":
        req.write_status(204, CORS_HEADERS)
        return
    req.write_json(200, {"method": req.command}, CORS_HEADERS)


def cookies(req):
    count = bounded(req, "count", 1, MAX_COOKIES)
    if count is None:
        req.write_json(400, {"error": f"count must be an integer between 0 and {MAX_COOKIES}"})
        return
    lines = [
        ("set-cookie", f"ocel-cookie-{i}=value-{i}; Path=/; HttpOnly") for i in range(1, count + 1)
    ]
    req.write_json(200, {"count": count}, lines)


def compress(req):
    body = json.dumps(COMPRESSIBLE, separators=(",", ":")).encode()
    extra = [("vary", "accept-encoding"), (CHECKSUM_FIELD, hashlib.sha256(body).hexdigest())]
    if GZIP_TOKEN.search(req.headers.get("accept-encoding") or ""):
        zipped = gzip.compress(body)
        req.write_bytes(200, "application/json", zipped, [*extra, ("content-encoding", "gzip")])
        return
    req.write_bytes(200, "application/json", body, extra)


def inflate(req):
    read = req.read_body()
    if read is None:
        refuse(req)
        return
    encoding = req.headers.get("content-encoding")
    if encoding is not None and encoding.strip().lower() == "gzip":
        read = gunzipped(read)
        if read is None:
            refuse(req)
            return
    req.write_json(
        200,
        {"encoding": encoding, "bytes": len(read), "sha256": hashlib.sha256(read).hexdigest()},
    )


def gunzipped(read):
    inflater = zlib.decompressobj(16 + zlib.MAX_WBITS)
    try:
        body = inflater.decompress(read, MAX_BODY + 1)
    except zlib.error:
        return None
    if len(body) > MAX_BODY or not inflater.eof:
        return None
    return body


def multipart(req):
    kind = req.headers.get("content-type") or ""
    if not kind.startswith("multipart/form-data"):
        req.write_json(415, {"error": "multipart/form-data only"})
        return
    read = req.read_body()
    if read is None:
        refuse(req)
        return
    form = BytesParser(policy=HTTP).parsebytes(
        b"content-type: " + kind.encode("latin-1") + b"\r\n\r\n" + read
    )
    if not form.is_multipart():
        refuse(req)
        return
    fields = {}
    files = []
    for part in form.iter_parts():
        field = part.get_param("name", header="content-disposition")
        value = part.get_payload(decode=True) or b""
        name = part.get_filename()
        if name is None:
            fields[field] = value.decode("utf-8", "replace")
            continue
        files.append(
            {
                "field": field,
                "name": name,
                "type": str(part.get("content-type") or ""),
                "bytes": len(value),
                "sha256": hashlib.sha256(value).hexdigest(),
            }
        )
    req.write_json(200, {"fields": fields, "files": files})


def take_large(req):
    read = req.read_body()
    if read is None:
        refuse(req)
        return
    req.write_json(200, {"bytes": len(read), "sha256": hashlib.sha256(read).hexdigest()})


def send_large(req):
    wanted = bounded(req, "bytes", 0, MAX_BODY)
    if wanted is None:
        req.write_json(400, {"error": f"bytes must be an integer between 0 and {MAX_BODY}"})
        return
    body = os.urandom(wanted)
    extra = [(CHECKSUM_FIELD, hashlib.sha256(body).hexdigest())]
    if "chunked" not in query_of(req):
        req.write_bytes(200, "application/octet-stream", body, extra)
        return
    req.start_chunks([("content-type", "application/octet-stream"), *extra])
    for offset in range(0, len(body), CHUNK_BYTES):
        req.write_chunk(body[offset : offset + CHUNK_BYTES])
    req.end_chunks()


def sleep(req):
    ms = bounded(req, "ms", 0, MAX_SLEEP_MS)
    if ms is None:
        req.write_json(400, {"error": f"ms must be an integer between 0 and {MAX_SLEEP_MS}"})
        return
    time.sleep(ms / 1000)
    req.write_json(200, {"slept": ms})


def vendored(req):
    req.write_json(
        200,
        {
            "dependency": "six",
            "version": six.__version__,
            "answer": len(list(six.moves.range(2))),
        },
    )


def refuse(req):
    req.write_json(400, {"error": "bad request"}, [("connection", "close")])


def path_of(req):
    return req.path.split("?", 1)[0]


def query_of(req):
    _, _, query = req.path.partition("?")
    return parse_qs(query, keep_blank_values=True)


def bounded(req, name, fallback, maximum):
    values = query_of(req).get(name)
    if not values:
        return fallback
    try:
        value = int(values[0])
    except ValueError:
        return None
    return value if 0 <= value <= maximum else None


def at(method, path):
    return lambda seen, asked: seen == method and asked == path


def any_method(path):
    return lambda _, asked: asked == path


def under(path):
    return lambda _, asked: asked == path or asked.startswith(path + "/")


PROBES = [
    (at("GET", "/api/probes/stream"), stream),
    (at("GET", "/api/probes/sse"), sse),
    (lambda seen, asked: seen == "GET" and STATUS_PATH.match(asked) is not None, status),
    (lambda seen, asked: seen == "GET" and EMPTY_PATH.match(asked) is not None, empty),
    (under("/api/probes/echo"), echo),
    (at("GET", "/api/probes/headers"), headers),
    (at("GET", "/api/probes/auth"), auth),
    (any_method("/api/probes/method"), method),
    (any_method("/api/probes/cors"), cors),
    (at("GET", "/api/probes/cookies"), cookies),
    (at("GET", "/api/probes/compress"), compress),
    (at("POST", "/api/probes/inflate"), inflate),
    (at("POST", "/api/probes/multipart"), multipart),
    (at("POST", "/api/probes/large"), take_large),
    (at("GET", "/api/probes/large"), send_large),
    (at("GET", "/api/probes/sleep"), sleep),
    (at("GET", "/api/probes/vendored"), vendored),
]
