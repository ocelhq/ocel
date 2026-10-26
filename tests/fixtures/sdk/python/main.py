import base64
import hmac
import json
import os
from pathlib import Path
from socketserver import ThreadingMixIn
from wsgiref.simple_server import WSGIRequestHandler, WSGIServer, make_server

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

import ocel
from infra import (
    Env,
    deploys,
    json_text,
    live,
    notices,
    orders,
    rooms,
    sighting,
    status,
    verbatim,
)

DEFAULT_PORT = "3104"
BINDING_KEY = f"OCEL_RESOURCE_REALTIME_{live.name}"
MAX_REQUEST_BYTES = 1 << 20
CHANNELS = {channel.pattern: channel for channel in (orders, deploys, rooms, status)}

serve_realtime = live.wsgi()


class ThreadingServer(ThreadingMixIn, WSGIServer):
    daemon_threads = True


class QuietHandler(WSGIRequestHandler):
    def log_message(self, format, *args):
        pass


def answer(start_response, status_line, body=None):
    content = b"" if body is None else json.dumps(body).encode()
    headers = [("content-length", str(len(content)))]
    if body is not None:
        headers.append(("content-type", "application/json"))
    start_response(status_line, headers)
    return [content]


def read_json(environ):
    length = min(int(environ.get("CONTENT_LENGTH") or 0), MAX_REQUEST_BYTES)
    return json.loads(environ["wsgi.input"].read(length))


def publish(environ, start_response):
    sent = read_json(environ)
    channel = CHANNELS.get(sent.get("pattern"))
    if channel is None:
        return answer(start_response, "400 Bad Request", {"error": "no such channel"})
    try:
        channel.publish(sent.get("body"), **sent.get("params", {}))
    except ocel.RealtimePublishError as error:
        return answer(start_response, "422 Unprocessable Entity", {"code": error.code})
    except Exception as error:
        return answer(start_response, "502 Bad Gateway", {"error": str(error)})
    return answer(start_response, "204 No Content")


def is_journey_nonce(given):
    return bool(given) and hmac.compare_digest(given, Env().journey_nonce.value)


def read_binding():
    live_dir = os.environ.get("OCEL_LIVE_DIR")
    if live_dir:
        delivered = Path(live_dir, BINDING_KEY)
        if delivered.is_file():
            return delivered.read_text()
    raw = os.environ.get(BINDING_KEY)
    if not raw:
        raise RuntimeError(f"{BINDING_KEY} was not delivered")
    return raw


def read_binding_seed():
    return json.loads(read_binding())["realtime"].get("signingKey", "")


def read_signing_key(signed_by):
    if signed_by == "binding":
        return Ed25519PrivateKey.from_private_bytes(base64.b64decode(read_binding_seed()))
    if signed_by == "another-key":
        return Ed25519PrivateKey.generate()
    if signed_by == "nobody":
        return None
    raise ValueError(f"no signer named {signed_by!r}")


def encode_segment(value):
    raw = json.dumps(value, separators=(",", ":"), ensure_ascii=False).encode()
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def sign_token(environ, start_response):
    if not is_journey_nonce(environ.get("HTTP_X_JOURNEY_NONCE", "")):
        return answer(
            start_response,
            "403 Forbidden",
            {"error": "signing a token needs the nonce the harness set"},
        )
    sent = read_json(environ)
    if sent["signedBy"] == "binding" and not read_binding_seed():
        return answer(
            start_response,
            "404 Not Found",
            {"error": "the realtime binding holds no signing key"},
        )
    signing_input = f"{encode_segment(sent['header'])}.{encode_segment(sent['claims'])}"
    key = read_signing_key(sent["signedBy"])
    signature = ""
    if key is not None:
        signed = key.sign(signing_input.encode())
        signature = base64.urlsafe_b64encode(signed).rstrip(b"=").decode()
    return answer(start_response, "200 OK", {"token": f"{signing_input}.{signature}"})


def wire(environ, start_response, path, method):
    try:
        if path == "/api/wire/trigger" and method == "POST":
            return answer(start_response, "200 OK", {"id": verbatim.trigger(read_json(environ)).id})
        if path == "/api/wire/send" and method == "POST":
            return answer(start_response, "200 OK", {"messageId": notices.send(read_json(environ))})
        if path.startswith("/api/wire/runs/") and method == "GET":
            run = ocel.runs.retrieve(path.removeprefix("/api/wire/runs/"))
            return answer(
                start_response,
                "200 OK",
                {
                    "id": run.id,
                    "task": run.task,
                    "status": run.status,
                    "payload": json_text(run.payload),
                    "output": "" if run.output is None else json_text(run.output),
                    "error": run.error,
                },
            )
        if path.startswith("/api/wire/sightings/") and method == "GET":
            tag = path.removeprefix("/api/wire/sightings/")
            page = ocel.runs.list(task=sighting, tags=[tag], status=ocel.RunStatus.COMPLETED)
            return answer(start_response, "200 OK", [run.output for run in page.runs])
    except Exception as error:
        return answer(start_response, "400 Bad Request", {"error": str(error)})
    return answer(start_response, "404 Not Found", {"error": f"no route {method} {path}"})


def app(environ, start_response):
    path, method = environ["PATH_INFO"], environ["REQUEST_METHOD"]
    if path == "/health" and method == "GET":
        return answer(start_response, "200 OK", {"ok": True, "app": "web"})
    if path == "/api/realtime":
        return serve_realtime(environ, start_response)
    if path == "/api/publish" and method == "POST":
        return publish(environ, start_response)
    if path == "/api/tokens" and method == "POST":
        return sign_token(environ, start_response)
    if path.startswith("/api/wire/"):
        return wire(environ, start_response, path, method)
    return answer(start_response, "404 Not Found", {"error": f"no route {method} {path}"})


def main():
    port = int(os.environ.get("PORT") or DEFAULT_PORT)
    print(f"python listening on http://localhost:{port}", flush=True)
    make_server("", port, app, ThreadingServer, QuietHandler).serve_forever()


if __name__ == "__main__":
    main()
