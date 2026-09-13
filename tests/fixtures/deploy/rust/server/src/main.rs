use std::collections::HashMap;
use std::convert::Infallible;
use std::time::Duration;

use axum::body::{Body, Bytes};
use axum::extract::{DefaultBodyLimit, Path, Query};
use axum::http::{header, HeaderMap, Method, StatusCode, Uri};
use axum::response::{IntoResponse, Response};
use axum::routing::{any, get, post};
use axum::Router;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};

const APP_NAME: &str = "web";

const DEFAULT_PORT: &str = "3106";

const OCEL_SVG: &str = r##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="ocel"><rect width="64" height="64" rx="14" fill="#0b0f14"/><circle cx="24" cy="27" r="5" fill="#f2b705"/><circle cx="42" cy="27" r="5" fill="#f2b705"/><path d="M20 42c4 5 20 5 24 0" stroke="#f2b705" stroke-width="4" fill="none" stroke-linecap="round"/></svg>
"##;

const MAX_SLEEP_MS: u64 = 30_000;
const MAX_BODY: usize = 8 << 20;
const STREAM_CHUNKS: u32 = 5;
const STREAM_GAP: Duration = Duration::from_millis(200);
const STREAM_END: &str = "ocel-stream-end";
const PROBE_HEADER: &str = "x-ocel-probe";
const CHECKSUM_HEADER: &str = "x-ocel-sha256";

#[tokio::main]
async fn main() {
    let port = std::env::var("PORT").unwrap_or_else(|_| DEFAULT_PORT.to_string());

    let app = Router::new()
        .route("/health", get(health))
        .route("/ocel.svg", get(logo))
        .route("/api/probes/stream", get(stream))
        .route("/api/probes/status/{code}", get(status))
        .route("/api/probes/empty/{kind}", get(empty))
        .route("/api/probes/echo", any(echo))
        .route("/api/probes/echo/{*rest}", any(echo))
        .route("/api/probes/large", post(take_large).get(send_large))
        .route("/api/probes/sleep", get(sleep))
        .layer(DefaultBodyLimit::max(MAX_BODY));

    let listener = tokio::net::TcpListener::bind(format!("0.0.0.0:{port}"))
        .await
        .expect("bind");
    println!("rust listening on http://localhost:{port}");
    axum::serve(listener, app).await.expect("serve");
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

async fn status(Path(code): Path<String>) -> Response {
    let Some(code) = code.parse::<u16>().ok().and_then(|c| StatusCode::from_u16(c).ok()) else {
        return json_response(StatusCode::BAD_REQUEST, json!({ "error": "code must be a status" }));
    };
    if code == StatusCode::NO_CONTENT {
        return code.into_response();
    }
    let mut response = json_response(code, json!({ "status": code.as_u16() }));
    if code.is_redirection() {
        response
            .headers_mut()
            .insert(header::LOCATION, "/api/probes/status/204".parse().unwrap());
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

async fn echo(
    method: Method,
    uri: Uri,
    Query(query): Query<HashMap<String, String>>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let header = headers
        .get(PROBE_HEADER)
        .and_then(|value| value.to_str().ok())
        .map_or(Value::Null, |value| Value::String(value.to_string()));
    json_response(
        StatusCode::OK,
        json!({
            "method": method.as_str(),
            "path": uri.path(),
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

async fn take_large(body: Bytes) -> Response {
    json_response(
        StatusCode::OK,
        json!({ "bytes": body.len(), "sha256": sha256_hex(&body) }),
    )
}

async fn send_large(Query(query): Query<HashMap<String, String>>) -> Response {
    let Some(bytes) = number_or(&query, "bytes", 0).filter(|n| *n <= MAX_BODY as u64) else {
        return json_response(
            StatusCode::BAD_REQUEST,
            json!({ "error": format!("bytes must be an integer between 0 and {MAX_BODY}") }),
        );
    };
    let mut body = vec![0u8; bytes as usize];
    if getrandom::fill(&mut body).is_err() {
        return json_response(StatusCode::INTERNAL_SERVER_ERROR, json!({ "error": "internal error" }));
    }
    (
        [
            (header::CONTENT_TYPE, "application/octet-stream".to_string()),
            (header::HeaderName::from_static(CHECKSUM_HEADER), sha256_hex(&body)),
            (header::CONTENT_LENGTH, body.len().to_string()),
        ],
        body,
    )
        .into_response()
}

async fn sleep(Query(query): Query<HashMap<String, String>>) -> Response {
    let Some(ms) = number_or(&query, "ms", 0).filter(|n| *n <= MAX_SLEEP_MS) else {
        return json_response(
            StatusCode::BAD_REQUEST,
            json!({ "error": format!("ms must be an integer between 0 and {MAX_SLEEP_MS}") }),
        );
    };
    tokio::time::sleep(Duration::from_millis(ms)).await;
    json_response(StatusCode::OK, json!({ "slept": ms }))
}

fn number_or(query: &HashMap<String, String>, name: &str, fallback: u64) -> Option<u64> {
    match query.get(name).filter(|value| !value.is_empty()) {
        Some(value) => value.parse().ok(),
        None => Some(fallback),
    }
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
