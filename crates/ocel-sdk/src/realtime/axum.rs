//! Serves a [`Realtime`] from an axum app, under the `axum` and `realtime` features.

use super::handler::{handle, MAX_REQUEST_BYTES};
use super::{Realtime, Request};
use axum::body::Body;
use bytes::Bytes;

/// A router serving `rt` to browsers on every path it is mounted at, as
/// [`Realtime::handle`] does:
///
/// ```ignore
/// let app = axum::Router::new().nest_service("/api/realtime", ocel::realtime::axum::router(rt));
/// ```
///
/// It stops reading a body as soon as it passes 1 MiB, and answers 413.
pub fn router(rt: Realtime) -> axum::Router {
    axum::Router::new().fallback(move |request: axum::extract::Request| {
        let rt = rt.clone();
        async move {
            let (parts, body) = request.into_parts();
            let read = axum::body::to_bytes(body, MAX_REQUEST_BYTES).await;
            let request = Request {
                method: parts.method,
                uri: parts.uri,
                headers: parts.headers,
                body: read.as_ref().cloned().unwrap_or_else(|_| Bytes::new()),
            };
            handle(&rt, request, read.is_err()).await.map(Body::from)
        }
    })
}
