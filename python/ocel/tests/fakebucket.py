import hashlib
import io
import socketserver
import threading
from urllib.parse import parse_qs, quote, unquote, urlparse
from wsgiref.simple_server import WSGIRequestHandler, WSGIServer, make_server

from connectrpc.code import Code
from connectrpc.errors import ConnectError
from protobuf.wkt import Timestamp

from ocel.gen.app.bucket.v1.bucket_connect import BucketServiceWSGIApplication
from ocel.gen.app.bucket.v1.bucket_pb import (
    AbortMultipartResponse,
    CompleteMultipartResponse,
    CopyResponse,
    CreateMultipartResponse,
    DeleteResponse,
    HeadResponse,
    ListResponse,
    ObjectInfo,
    PresignedTarget,
    SignedOperation,
    SignedPart,
    SignPartsResponse,
    SignResponse,
)

TOKEN = "letmein"

_UPLOADED_AT = Timestamp(seconds=1_700_000_000, nanos=0)


class Held:
    def __init__(self, data, content_type, metadata, cache_control=""):
        self.data = data
        self.content_type = content_type
        self.metadata = dict(metadata)
        self.cache_control = cache_control

    @property
    def etag(self):
        return f'"{hashlib.md5(self.data).hexdigest()}"'


class Store:
    def __init__(self):
        self.objects: dict[str, Held] = {}
        self.uploads: dict[str, dict] = {}
        self.aborted: list[str] = []
        self.page_size = 2
        self.refuse_part: int | None = None
        self.signed: list[tuple] = []
        self.listed: list[tuple] = []
        self.authorizations: list[str | None] = []

    def info(self, key: str) -> ObjectInfo:
        held = self.objects[key]
        return ObjectInfo(
            key=key,
            size=len(held.data),
            etag=held.etag,
            content_type=held.content_type,
            uploaded_at=_UPLOADED_AT,
            metadata=dict(held.metadata),
        )


class Service:
    def __init__(self, store: Store, base: list[str]):
        self.store = store
        self.base = base

    def _url(self, path: str) -> str:
        return f"{self.base[0]}{path}"

    def head(self, request, ctx):
        self._seen(ctx)
        if request.key not in self.store.objects:
            return HeadResponse()
        return HeadResponse(object=self.store.info(request.key))

    def list(self, request, ctx):
        self._seen(ctx)
        self.store.listed.append((request.prefix, request.limit, request.cursor))
        keys = sorted(k for k in self.store.objects if k.startswith(request.prefix))
        start = keys.index(request.cursor) if request.cursor in keys else 0
        size = request.limit or self.store.page_size
        page = keys[start : start + size]
        rest = keys[start + size :]
        return ListResponse(
            objects=[self.store.info(key) for key in page],
            next_cursor=rest[0] if rest else "",
        )

    def delete(self, request, ctx):
        self._seen(ctx)
        for key in request.keys:
            self.store.objects.pop(key, None)
        return DeleteResponse()

    def copy(self, request, ctx):
        self._seen(ctx)
        held = self.store.objects.get(request.source_key)
        if held is None:
            raise ConnectError(Code.NOT_FOUND, f"no object under {request.source_key}")
        self.store.objects[request.destination_key] = Held(
            held.data, held.content_type, held.metadata
        )
        return CopyResponse(object=self.store.info(request.destination_key))

    def sign(self, request, ctx):
        self._seen(ctx)
        expires = request.expires_in.to_seconds() if request.expires_in else 0
        self.store.signed.append((request.key, request.operation, request.audience, expires))
        query = [f"expires={expires:g}"]
        constraints = request.constraints
        if constraints and constraints.download_filename:
            query.append(f"download={quote(constraints.download_filename)}")
        if constraints and constraints.max_size:
            query.append(f"max={constraints.max_size}")
        if constraints and constraints.content_type:
            query.append(f"type={quote(constraints.content_type)}")
        url = self._url(f"/objects/{quote(request.key)}?{'&'.join(query)}")
        if request.operation is SignedOperation.POST_UPLOAD:
            return SignResponse(
                target=PresignedTarget(
                    url=url,
                    key=request.key,
                    method="POST",
                    fields={"key": request.key},
                    headers={"x-fake-signature": "signed"},
                )
            )
        method = "PUT" if request.operation is SignedOperation.PUT else "GET"
        headers = {"x-fake-signature": "signed"}
        if constraints:
            for name, value in constraints.metadata.items():
                headers[f"x-amz-meta-{name}"] = value
        return SignResponse(
            target=PresignedTarget(url=url, key=request.key, method=method, headers=headers)
        )

    def create_multipart(self, request, ctx):
        self._seen(ctx)
        upload_id = f"upload-{len(self.store.uploads) + 1}"
        self.store.uploads[upload_id] = {
            "key": request.key,
            "content_type": request.content_type,
            "cache_control": request.cache_control,
            "metadata": dict(request.metadata),
            "parts": {},
        }
        return CreateMultipartResponse(upload_id=upload_id)

    def sign_parts(self, request, ctx):
        self._seen(ctx)
        return SignPartsResponse(
            parts=[
                SignedPart(
                    part_number=number,
                    url=self._url(f"/parts/{request.upload_id}/{number}"),
                    headers={"x-fake-signature": "signed"},
                )
                for number in request.part_numbers
            ]
        )

    def complete_multipart(self, request, ctx):
        self._seen(ctx)
        upload = self.store.uploads.pop(request.upload_id)
        data = b"".join(upload["parts"][part.part_number] for part in request.parts)
        self._refuse_unmet(request.key, request.if_none_match, request.if_match)
        self.store.objects[request.key] = Held(
            data, upload["content_type"], upload["metadata"], upload["cache_control"]
        )
        return CompleteMultipartResponse(object=self.store.info(request.key))

    def abort_multipart(self, request, ctx):
        self._seen(ctx)
        self.store.uploads.pop(request.upload_id, None)
        self.store.aborted.append(request.upload_id)
        return AbortMultipartResponse()

    def presign_upload(self, request, ctx):
        raise ConnectError(Code.UNIMPLEMENTED, "presign_upload")

    def verify_upload_signature(self, request, ctx):
        raise ConnectError(Code.UNIMPLEMENTED, "verify_upload_signature")

    def get_upload_status(self, request, ctx):
        raise ConnectError(Code.UNIMPLEMENTED, "get_upload_status")

    def complete_upload(self, request, ctx):
        raise ConnectError(Code.UNIMPLEMENTED, "complete_upload")

    def _refuse_unmet(self, key, if_none_match, if_match):
        held = self.store.objects.get(key)
        if if_none_match == "*" and held is not None:
            raise ConnectError(Code.FAILED_PRECONDITION, f"{key} is already held")
        if if_match and (held is None or held.etag != if_match):
            raise ConnectError(Code.FAILED_PRECONDITION, f"{key} does not match {if_match}")

    def _seen(self, ctx):
        self.store.authorizations.append(ctx.request_headers.get("Authorization"))


class Bucket:
    def __init__(self):
        self.store = Store()
        self.base = [""]
        self._rpc = BucketServiceWSGIApplication(Service(self.store, self.base))
        self.server = make_server(
            "127.0.0.1", 0, self._dispatch, server_class=_Threading, handler_class=_Quiet
        )
        self.base[0] = f"http://127.0.0.1:{self.server.server_address[1]}"
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    @property
    def url(self):
        return self.base[0]

    def close(self):
        self.server.shutdown()
        self.server.server_close()

    def _dispatch(self, environ, start_response):
        path = environ.get("PATH_INFO", "")
        if path.startswith("/app.bucket.v1."):
            environ["wsgi.input"] = io.BytesIO(_body(environ))
            return self._rpc(environ, start_response)
        if path.startswith("/parts/"):
            return self._part(environ, start_response)
        return self._object(environ, start_response)

    def _part(self, environ, start_response):
        _, _, upload_id, number = environ["PATH_INFO"].split("/")
        number = int(number)
        if self.store.refuse_part == number:
            start_response("500 Internal Server Error", [("content-length", "0")])
            return [b""]
        body = _body(environ)
        self.store.uploads[upload_id]["parts"][number] = body
        etag = f'"{hashlib.md5(body).hexdigest()}"'
        start_response("200 OK", [("etag", etag), ("content-length", "0")])
        return [b""]

    def _object(self, environ, start_response):
        key = unquote(environ["PATH_INFO"].lstrip("/").removeprefix("objects/"))
        method = environ["REQUEST_METHOD"]
        if method in ("PUT", "POST"):
            return self._write(key, environ, start_response)
        return self._read(key, environ, start_response)

    def _write(self, key, environ, start_response):
        held = self.store.objects.get(key)
        if environ.get("HTTP_IF_NONE_MATCH") == "*" and held is not None:
            return _status("412 Precondition Failed", start_response)
        if_match = environ.get("HTTP_IF_MATCH")
        if if_match and (held is None or held.etag != if_match):
            return _status("412 Precondition Failed", start_response)
        query = parse_qs(urlparse("?" + environ.get("QUERY_STRING", "")).query)
        body = _body(environ)
        if "max" in query and len(body) > int(query["max"][0]):
            return _status("413 Content Too Large", start_response)
        metadata = {
            name[len("HTTP_X_AMZ_META_") :].lower().replace("_", "-"): value
            for name, value in environ.items()
            if name.startswith("HTTP_X_AMZ_META_")
        }
        stored = Held(
            body,
            environ.get("CONTENT_TYPE", ""),
            metadata,
            environ.get("HTTP_CACHE_CONTROL", ""),
        )
        self.store.objects[key] = stored
        start_response("200 OK", [("etag", stored.etag), ("content-length", "0")])
        return [b""]

    def _read(self, key, environ, start_response):
        held = self.store.objects.get(key)
        if held is None:
            return _status("404 Not Found", start_response)
        data = held.data
        status = "200 OK"
        asked = environ.get("HTTP_RANGE", "")
        if asked.startswith("bytes="):
            first, _, last = asked[len("bytes=") :].partition("-")
            start = int(first)
            end = int(last) + 1 if last else len(data)
            data = data[start:end]
            status = "206 Partial Content"
        start_response(
            status,
            [
                ("content-type", held.content_type or "application/octet-stream"),
                ("content-length", str(len(data))),
                ("etag", held.etag),
            ],
        )
        return [data]


def _status(status, start_response):
    start_response(status, [("content-length", "0")])
    return [b""]


def _body(environ):
    length = int(environ.get("CONTENT_LENGTH") or 0)
    return environ["wsgi.input"].read(length)


class _Threading(socketserver.ThreadingMixIn, WSGIServer):
    daemon_threads = True


class _Quiet(WSGIRequestHandler):
    def log_message(self, *_args):
        pass
