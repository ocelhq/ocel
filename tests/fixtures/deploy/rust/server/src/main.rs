use std::convert::Infallible;
use std::io::Read;
use std::net::SocketAddr;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use axum::body::{Body, Bytes};
use axum::extract::{ConnectInfo, DefaultBodyLimit, FromRequest, Multipart, Path, Query, Request};
use axum::http::{header, HeaderMap, HeaderName, HeaderValue, Method, StatusCode, Uri};
use axum::response::{IntoResponse, Response};
use axum::routing::{any, get, post};
use axum::Router;
use flate2::read::{GzDecoder, ZlibDecoder};
use flate2::write::GzEncoder;
use flate2::Compression;
use serde_json::{json, Map, Value};
use sha2::{Digest, Sha256};

const APP_NAME: &str = "web";

const DEFAULT_PORT: &str = "3106";

const OCEL_SVG: &str = r##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="ocel"><rect width="64" height="64" rx="14" fill="#0b0f14"/><circle cx="24" cy="27" r="5" fill="#f2b705"/><circle cx="42" cy="27" r="5" fill="#f2b705"/><path d="M20 42c4 5 20 5 24 0" stroke="#f2b705" stroke-width="4" fill="none" stroke-linecap="round"/></svg>
"##;

const MAX_SLEEP_MS: u64 = 60_000;
const MAX_BYTES: usize = 8 * 1024 * 1024;
const STREAM_CHUNKS: u32 = 5;
const STREAM_GAP: Duration = Duration::from_millis(200);
const STREAM_END: &str = "ocel-stream-end";
const CHUNK_BYTES: usize = 64 * 1024;
const MAX_COOKIES: u64 = 10;
const MAX_EVENTS: u64 = 10;
const MAX_GAP_MS: u64 = 60_000;
const ECHO_PREFIX: &str = "/api/probes/echo/";
const PROBE_HEADER: &str = "x-ocel-probe";
const CHECKSUM_HEADER: &str = "x-ocel-sha256";

#[tokio::main]
async fn main() {
    let port = std::env::var("PORT").unwrap_or_else(|_| DEFAULT_PORT.to_string());

    let app = Router::new()
        .route("/health", get(health))
        .route("/ocel.svg", get(logo))
        .route("/api/probes/stream", get(stream))
        .route("/api/probes/sse", get(sse))
        .route("/api/probes/status/{code}", get(status))
        .route("/api/probes/empty/{kind}", get(empty))
        .route("/api/probes/echo", any(echo))
        .route("/api/probes/echo/{*rest}", any(echo))
        .route("/api/probes/headers", get(headers_probe))
        .route("/api/probes/auth", get(auth))
        .route("/api/probes/method", any(method_probe))
        .route("/api/probes/cors", any(cors))
        .route("/api/probes/cookies", get(cookies))
        .route("/api/probes/compress", get(compress))
        .route("/api/probes/inflate", post(inflate))
        .route("/api/probes/multipart", post(multipart))
        .route("/api/probes/large", post(take_large).get(send_large))
        .route("/api/probes/sleep", get(sleep))
        .layer(DefaultBodyLimit::max(MAX_BYTES));

    let listener = tokio::net::TcpListener::bind(format!("0.0.0.0:{port}"))
        .await
        .expect("bind");
    println!("rust listening on http://localhost:{port}");
    axum::serve(
        listener,
        app.into_make_service_with_connect_info::<SocketAddr>(),
    )
    .await
    .expect("serve");
}

async fn health() -> Response {
    json_response(StatusCode::OK, json!({ "ok": true, "app": APP_NAME }))
}

async fn logo() -> Response {
    (
        [
            (header::CONTENT_TYPE, "image/svg+xml"),
            (header::CONTENT_LENGTH, &OCEL_SVG.len().to_string()),
        ],
        OCEL_SVG,
    )
        .into_response()
}

async fn stream() -> Response {
    let chunks = futures_util::stream::unfold(1u32, |i| async move {
        if i > STREAM_CHUNKS {
            return None;
        }
        if i > 1 {
            tokio::time::sleep(STREAM_GAP).await;
        }
        let line = if i < STREAM_CHUNKS {
            format!("ocel-stream-{i}\n")
        } else {
            format!("{STREAM_END}\n")
        };
        Some((Ok::<_, Infallible>(Bytes::from(line)), i + 1))
    });
    (
        [
            (header::CONTENT_TYPE, "text/plain; charset=utf-8"),
            (header::CACHE_CONTROL, "no-store, no-transform"),
        ],
        Body::from_stream(chunks),
    )
        .into_response()
}

async fn sse(uri: Uri) -> Response {
    let pairs = query_pairs(&uri);
    let events = bounded(first(&pairs, "events"), 3, MAX_EVENTS);
    let gap = bounded(first(&pairs, "gap"), 1000, MAX_GAP_MS);
    let (Some(events), Some(gap)) = (events, gap) else {
        return json_response(
            StatusCode::BAD_REQUEST,
            json!({ "error": format!("events must be 0..{MAX_EVENTS} and gap 0..{MAX_GAP_MS}") }),
        );
    };
    let frames = futures_util::stream::unfold(1u64, move |i| async move {
        if i > events + 1 {
            return None;
        }
        if i == events + 1 {
            let end = Bytes::from_static(b"event: end\ndata: ocel-sse-end\n\n");
            return Some((Ok::<_, Infallible>(end), i + 1));
        }
        if i > 1 {
            tokio::time::sleep(Duration::from_millis(gap)).await;
        }
        let frame = format!("id: {i}\ndata: ocel-sse-{i} {}\n\n", epoch_ms());
        Some((Ok(Bytes::from(frame)), i + 1))
    });
    (
        [
            (header::CONTENT_TYPE, "text/event-stream"),
            (header::CACHE_CONTROL, "no-store, no-transform"),
        ],
        Body::from_stream(frames),
    )
        .into_response()
}

async fn status(Path(code): Path<String>) -> Response {
    let Some(code) = code
        .parse::<u16>()
        .ok()
        .and_then(|c| StatusCode::from_u16(c).ok())
    else {
        return json_response(
            StatusCode::BAD_REQUEST,
            json!({ "error": "code must be a status" }),
        );
    };
    if code == StatusCode::NO_CONTENT {
        return code.into_response();
    }
    let mut response = json_response(code, json!({ "status": code.as_u16() }));
    if code.is_redirection() {
        response.headers_mut().insert(
            header::LOCATION,
            HeaderValue::from_static("/api/probes/status/204"),
        );
    }
    response
}

async fn empty(Path(kind): Path<String>) -> Response {
    match kind.as_str() {
        "redirect" => (
            StatusCode::FOUND,
            [
                (header::LOCATION, "/api/probes/status/204"),
                (header::CONTENT_LENGTH, "0"),
            ],
        )
            .into_response(),
        "ok" => (StatusCode::OK, [(header::CONTENT_LENGTH, "0")]).into_response(),
        other => json_response(
            StatusCode::NOT_FOUND,
            json!({ "error": format!("no empty probe called {other}") }),
        ),
    }
}

async fn echo(method: Method, uri: Uri, headers: HeaderMap, body: Bytes) -> Response {
    let mut query = Map::new();
    for (key, value) in query_pairs(&uri) {
        query.entry(key).or_insert(Value::String(value));
    }
    let segments: Vec<String> = match uri.path().strip_prefix(ECHO_PREFIX) {
        Some(rest) if !rest.is_empty() => rest.split('/').map(percent_decode).collect(),
        _ => Vec::new(),
    };
    let header = headers
        .get(PROBE_HEADER)
        .map_or(Value::Null, |value| Value::String(header_text(value)));
    json_response(
        StatusCode::OK,
        json!({
            "method": method.as_str(),
            "path": uri.path(),
            "search": uri.query().map_or(String::new(), |search| format!("?{search}")),
            "segments": segments,
            "query": query,
            "header": header,
            "body": echoed_body(&headers, &body),
        }),
    )
}

fn echoed_body(headers: &HeaderMap, body: &[u8]) -> Value {
    if body.is_empty() {
        return Value::Null;
    }
    let is_json = headers
        .get(header::CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.contains("application/json"));
    if is_json {
        if let Ok(decoded) = serde_json::from_slice(body) {
            return decoded;
        }
    }
    Value::String(String::from_utf8_lossy(body).into_owned())
}

async fn headers_probe(ConnectInfo(peer): ConnectInfo<SocketAddr>, headers: HeaderMap) -> Response {
    let mut seen = Map::new();
    for name in headers.keys() {
        let joined = headers
            .get_all(name)
            .iter()
            .map(header_text)
            .collect::<Vec<_>>()
            .join(", ");
        seen.insert(name.as_str().to_string(), Value::String(joined));
    }
    let host = headers
        .get(header::HOST)
        .map(header_text)
        .unwrap_or_default();
    let remote = peer.ip().to_canonical().to_string();
    json_response(
        StatusCode::OK,
        json!({
            "headers": seen,
            "remote": remote,
            "ip": remote,
            "protocol": "http",
            "hostname": hostname_of(&host),
        }),
    )
}

async fn auth() -> Response {
    let mut response = json_response(StatusCode::UNAUTHORIZED, json!({ "error": "unauthorized" }));
    let headers = response.headers_mut();
    headers.insert(
        header::WWW_AUTHENTICATE,
        HeaderValue::from_static("Bearer realm=\"ocel\""),
    );
    headers.insert(
        HeaderName::from_static("x-ocel-number"),
        HeaderValue::from_static("42"),
    );
    response
}

async fn method_probe(method: Method) -> Response {
    if method != Method::GET && method != Method::HEAD {
        let mut response = json_response(
            StatusCode::METHOD_NOT_ALLOWED,
            json!({ "error": format!("{method} is not allowed") }),
        );
        response
            .headers_mut()
            .insert(header::ALLOW, HeaderValue::from_static("GET, HEAD"));
        return response;
    }
    json_response(StatusCode::OK, json!({ "method": method.as_str() }))
}

async fn cors(method: Method) -> Response {
    let mut response = if method == Method::OPTIONS {
        StatusCode::NO_CONTENT.into_response()
    } else {
        json_response(StatusCode::OK, json!({ "method": method.as_str() }))
    };
    let headers = response.headers_mut();
    headers.insert(
        header::ACCESS_CONTROL_ALLOW_ORIGIN,
        HeaderValue::from_static("*"),
    );
    headers.insert(
        header::ACCESS_CONTROL_ALLOW_METHODS,
        HeaderValue::from_static("GET, POST, OPTIONS"),
    );
    headers.insert(
        header::ACCESS_CONTROL_ALLOW_HEADERS,
        HeaderValue::from_static("content-type, x-ocel-probe"),
    );
    response
}

async fn cookies(uri: Uri) -> Response {
    let Some(count) = bounded(first(&query_pairs(&uri), "count"), 1, MAX_COOKIES) else {
        return json_response(
            StatusCode::BAD_REQUEST,
            json!({ "error": format!("count must be an integer between 0 and {MAX_COOKIES}") }),
        );
    };
    let mut response = json_response(StatusCode::OK, json!({ "count": count }));
    for i in 1..=count {
        let cookie = format!("ocel-cookie-{i}=value-{i}; Path=/; HttpOnly");
        response.headers_mut().append(
            header::SET_COOKIE,
            HeaderValue::from_str(&cookie).expect("cookie is a header value"),
        );
    }
    response
}

async fn compress(headers: HeaderMap) -> Response {
    let body = format!(
        "{{\"marker\":\"ocel-compress\",\"filler\":\"{}\"}}",
        "ocel ".repeat(1024)
    )
    .into_bytes();
    let checksum = sha256_hex(&body);
    let wants_gzip = headers
        .get(header::ACCEPT_ENCODING)
        .map(header_text)
        .is_some_and(|accepted| {
            accepted
                .split(|c: char| !(c.is_ascii_alphanumeric() || c == '_'))
                .any(|word| word == "gzip")
        });
    let (encoding, sent) = if wants_gzip {
        let mut encoder = GzEncoder::new(Vec::new(), Compression::default());
        std::io::Write::write_all(&mut encoder, &body).expect("gzip into memory");
        (Some("gzip"), encoder.finish().expect("gzip into memory"))
    } else {
        (None, body)
    };
    let mut response = (
        [
            (header::CONTENT_TYPE, "application/json".to_string()),
            (header::VARY, "accept-encoding".to_string()),
            (HeaderName::from_static(CHECKSUM_HEADER), checksum),
            (header::CONTENT_LENGTH, sent.len().to_string()),
        ],
        sent,
    )
        .into_response();
    if let Some(encoding) = encoding {
        response
            .headers_mut()
            .insert(header::CONTENT_ENCODING, HeaderValue::from_static(encoding));
    }
    response
}

async fn inflate(headers: HeaderMap, body: Bytes) -> Response {
    let encoding = headers.get(header::CONTENT_ENCODING).map(header_text);
    let decoded = match encoding.as_deref().map(str::to_ascii_lowercase).as_deref() {
        None | Some("identity") => Ok(body.to_vec()),
        Some("gzip") => read_capped(GzDecoder::new(&body[..])),
        Some("deflate") => read_capped(ZlibDecoder::new(&body[..])),
        Some(other) => {
            return json_response(
                StatusCode::UNSUPPORTED_MEDIA_TYPE,
                json!({ "error": format!("unsupported content encoding \"{other}\"") }),
            )
        }
    };
    let decoded = match decoded {
        Ok(decoded) if decoded.len() > MAX_BYTES => {
            return json_response(
                StatusCode::PAYLOAD_TOO_LARGE,
                json!({ "error": "request entity too large" }),
            )
        }
        Ok(decoded) => decoded,
        Err(_) => {
            return json_response(
                StatusCode::BAD_REQUEST,
                json!({ "error": "the body does not decode" }),
            )
        }
    };
    json_response(
        StatusCode::OK,
        json!({
            "encoding": encoding,
            "bytes": decoded.len(),
            "sha256": sha256_hex(&decoded),
        }),
    )
}

fn read_capped(decoder: impl Read) -> std::io::Result<Vec<u8>> {
    let mut decoded = Vec::new();
    decoder
        .take(MAX_BYTES as u64 + 1)
        .read_to_end(&mut decoded)?;
    Ok(decoded)
}

async fn multipart(request: Request) -> Response {
    let is_form = request
        .headers()
        .get(header::CONTENT_TYPE)
        .map(header_text)
        .is_some_and(|value| value.starts_with("multipart/form-data"));
    if !is_form {
        return json_response(
            StatusCode::UNSUPPORTED_MEDIA_TYPE,
            json!({ "error": "multipart/form-data only" }),
        );
    }
    let mut form = match Multipart::from_request(request, &()).await {
        Ok(form) => form,
        Err(rejection) => return rejection.into_response(),
    };
    let mut fields = Map::new();
    let mut files = Vec::new();
    loop {
        let field = match form.next_field().await {
            Ok(Some(field)) => field,
            Ok(None) => break,
            Err(error) => return error.into_response(),
        };
        let name = field.name().unwrap_or_default().to_string();
        let Some(file_name) = field.file_name().map(str::to_string) else {
            match field.text().await {
                Ok(text) => fields.insert(name, Value::String(text)),
                Err(error) => return error.into_response(),
            };
            continue;
        };
        let content_type = field.content_type().unwrap_or_default().to_string();
        let bytes = match field.bytes().await {
            Ok(bytes) => bytes,
            Err(error) => return error.into_response(),
        };
        files.push(json!({
            "field": name,
            "name": file_name,
            "type": content_type,
            "bytes": bytes.len(),
            "sha256": sha256_hex(&bytes),
        }));
    }
    json_response(StatusCode::OK, json!({ "fields": fields, "files": files }))
}

async fn take_large(body: Bytes) -> Response {
    json_response(
        StatusCode::OK,
        json!({ "bytes": body.len(), "sha256": sha256_hex(&body) }),
    )
}

async fn send_large(uri: Uri) -> Response {
    let pairs = query_pairs(&uri);
    let Some(bytes) = bounded(first(&pairs, "bytes"), 0, MAX_BYTES as u64) else {
        return json_response(
            StatusCode::BAD_REQUEST,
            json!({ "error": format!("bytes must be an integer between 0 and {MAX_BYTES}") }),
        );
    };
    let mut body = vec![0u8; bytes as usize];
    if getrandom::fill(&mut body).is_err() {
        return json_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            json!({ "error": "internal error" }),
        );
    }
    let checksum = sha256_hex(&body);
    let chunked = pairs.iter().any(|(key, _)| key == "chunked");
    if !chunked {
        return (
            [
                (header::CONTENT_TYPE, "application/octet-stream".to_string()),
                (HeaderName::from_static(CHECKSUM_HEADER), checksum),
                (header::CONTENT_LENGTH, body.len().to_string()),
            ],
            body,
        )
            .into_response();
    }
    let body = Bytes::from(body);
    let pieces = (0..body.len())
        .step_by(CHUNK_BYTES)
        .map(move |offset| {
            Ok::<_, Infallible>(body.slice(offset..(offset + CHUNK_BYTES).min(body.len())))
        })
        .collect::<Vec<_>>();
    (
        [
            (header::CONTENT_TYPE, "application/octet-stream".to_string()),
            (HeaderName::from_static(CHECKSUM_HEADER), checksum),
        ],
        Body::from_stream(futures_util::stream::iter(pieces)),
    )
        .into_response()
}

async fn sleep(uri: Uri) -> Response {
    let Some(ms) = bounded(first(&query_pairs(&uri), "ms"), 0, MAX_SLEEP_MS) else {
        return json_response(
            StatusCode::BAD_REQUEST,
            json!({ "error": format!("ms must be an integer between 0 and {MAX_SLEEP_MS}") }),
        );
    };
    tokio::time::sleep(Duration::from_millis(ms)).await;
    json_response(StatusCode::OK, json!({ "slept": ms }))
}

fn query_pairs(uri: &Uri) -> Vec<(String, String)> {
    Query::<Vec<(String, String)>>::try_from_uri(uri)
        .map(|Query(pairs)| pairs)
        .unwrap_or_default()
}

fn first<'a>(pairs: &'a [(String, String)], name: &str) -> Option<&'a str> {
    pairs
        .iter()
        .find(|(key, _)| key == name)
        .map(|(_, value)| value.as_str())
}

fn bounded(raw: Option<&str>, fallback: u64, max: u64) -> Option<u64> {
    let Some(raw) = raw else {
        return Some(fallback);
    };
    let raw = raw.trim();
    let value = if raw.is_empty() {
        0.0
    } else {
        raw.parse::<f64>().ok()?
    };
    (value.fract() == 0.0 && (0.0..=max as f64).contains(&value)).then_some(value as u64)
}

fn percent_decode(raw: &str) -> String {
    let bytes = raw.as_bytes();
    let mut decoded = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        let pair = bytes
            .get(i + 1..i + 3)
            .filter(|_| bytes[i] == b'%')
            .and_then(|hex| Some(hex_value(hex[0])? * 16 + hex_value(hex[1])?));
        match pair {
            Some(byte) => {
                decoded.push(byte);
                i += 3;
            }
            None => {
                decoded.push(bytes[i]);
                i += 1;
            }
        }
    }
    String::from_utf8_lossy(&decoded).into_owned()
}

fn hex_value(digit: u8) -> Option<u8> {
    (digit as char).to_digit(16).map(|value| value as u8)
}

fn hostname_of(host: &str) -> &str {
    if host.starts_with('[') {
        return host.find(']').map_or(host, |end| &host[..=end]);
    }
    host.split(':').next().unwrap_or(host)
}

fn header_text(value: &HeaderValue) -> String {
    String::from_utf8_lossy(value.as_bytes()).into_owned()
}

fn epoch_ms() -> u128 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |elapsed| elapsed.as_millis())
}

fn sha256_hex(bytes: &[u8]) -> String {
    Sha256::digest(bytes)
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

fn json_response(status: StatusCode, body: Value) -> Response {
    let encoded = body.to_string();
    (
        status,
        [
            (header::CONTENT_TYPE, "application/json".to_string()),
            (header::CONTENT_LENGTH, encoded.len().to_string()),
        ],
        encoded,
    )
        .into_response()
}
