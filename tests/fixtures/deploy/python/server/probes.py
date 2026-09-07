import hashlib
import json
import os
import re
import time
from urllib.parse import parse_qs, urlsplit

import six

MAX_SLEEP_MS = 30_000

MAX_BODY = 8 << 20

STREAM_CHUNKS = 5

STREAM_GAP = 0.2

STREAM_END = b"ocel-stream-end\n"

PROBE_HEADER = "x-ocel-probe"

CHECKSUM_FIELD = "x-ocel-sha256"

STATUS_PATH = re.compile(r"^/api/probes/status/(\d+)$")


def stream(req):
    req.start_chunks(
        {"content-type": "text/plain; charset=utf-8", "cache-control": "no-store, no-transform"}
    )
    for i in range(1, STREAM_CHUNKS):
        req.write_chunk(f"ocel-stream-{i}\n".encode())
        time.sleep(STREAM_GAP)
    req.write_chunk(STREAM_END)
    req.end_chunks()


def status(req):
    code = int(STATUS_PATH.match(path_of(req)).group(1))
    if code == 204:
        req.write_status(204)
        return
    extra = {"location": "/api/probes/status/204"} if 300 <= code < 400 else None
    req.write_bytes(code, "application/json", json.dumps({"status": code}).encode(), extra)


def echo(req):
    read = req.read_body()
    if read is None:
        refuse(req)
        return
    query = {name: values[0] for name, values in parse_qs(urlsplit(req.path).query).items()}
    req.write_json(
        200,
        {
            "method": req.command,
            "path": path_of(req),
            "query": query,
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


def take_large(req):
    read = req.read_body()
    if read is None:
        refuse(req)
        return
    req.write_json(200, {"bytes": len(read), "sha256": hashlib.sha256(read).hexdigest()})


def send_large(req):
    wanted = number_of(req, "bytes")
    if wanted is None or wanted < 0 or wanted > MAX_BODY:
        req.write_json(400, {"error": f"bytes must be an integer between 0 and {MAX_BODY}"})
        return
    body = os.urandom(wanted)
    req.write_bytes(
        200,
        "application/octet-stream",
        body,
        {CHECKSUM_FIELD: hashlib.sha256(body).hexdigest()},
    )


def sleep(req):
    ms = number_of(req, "ms")
    if ms is None or ms < 0 or ms > MAX_SLEEP_MS:
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
    req.write_bytes(
        400,
        "application/json",
        json.dumps({"error": "bad request"}).encode(),
        {"connection": "close"},
    )


def path_of(req):
    return req.path.split("?", 1)[0]


def number_of(req, name):
    values = parse_qs(urlsplit(req.path).query).get(name)
    if not values:
        return 0
    try:
        return int(values[0])
    except ValueError:
        return None


def at(method, path):
    return lambda seen, asked: seen == method and asked == path


def under(path):
    return lambda _, asked: asked == path or asked.startswith(path + "/")


PROBES = [
    (at("GET", "/api/probes/stream"), stream),
    (lambda seen, asked: seen == "GET" and STATUS_PATH.match(asked) is not None, status),
    (under("/api/probes/echo"), echo),
    (at("POST", "/api/probes/large"), take_large),
    (at("GET", "/api/probes/large"), send_large),
    (at("GET", "/api/probes/sleep"), sleep),
    (at("GET", "/api/probes/vendored"), vendored),
]
