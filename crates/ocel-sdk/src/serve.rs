use std::convert::Infallible;
use std::sync::Arc;
use std::time::Duration;

use bytes::Bytes;
use http::{header, Method, Request, Response, StatusCode};
use http_body_util::{BodyExt, Full};
use hyper::body::Incoming;
use hyper::server::conn::http1;
use hyper::service::service_fn;
use hyper_util::rt::TokioIo;
use tokio::net::TcpListener;

use crate::deliver::deliver;
use crate::error::Error;

const WORKER_ENV: &str = "OCEL_WORKER";
const HOST_ENV: &str = "HOST";
const PORT_ENV: &str = "PORT";
const LOOPBACK: &str = "127.0.0.1";
const ACCEPT_RETRY: Duration = Duration::from_millis(100);

/// Serve the deliveries made to the worker the process's `OCEL_WORKER` names, and never
/// return while it does. A process that names no worker gets `Ok(false)` back at once.
///
/// A binary whose `main` has [`macro@main`](crate::main) needs no call: it serves its tasks
/// and consumers when Ocel runs it as a worker, without entering the body of `main`. It
/// listens on `$PORT`, on the address `$HOST` names or on loopback. It blocks the calling
/// thread, and may be called from inside an async runtime or outside one.
pub fn serve_worker() -> Result<bool, Error> {
    let worker = match std::env::var(WORKER_ENV) {
        Ok(worker) if !worker.is_empty() => worker,
        _ => return Ok(false),
    };
    let host = std::env::var(HOST_ENV)
        .ok()
        .filter(|host| !host.is_empty())
        .unwrap_or_else(|| LOOPBACK.to_string());
    let port = std::env::var(PORT_ENV).unwrap_or_default();
    let refuse = |said: String| Error::Listen {
        worker: worker.clone(),
        address: format!("{host}:{port}"),
        said,
    };
    let port: u16 = port
        .parse()
        .map_err(|_| refuse(format!("{PORT_ENV} is not a port number")))?;

    std::thread::scope(|scope| {
        scope
            .spawn(|| {
                let runtime = tokio::runtime::Builder::new_multi_thread()
                    .enable_all()
                    .build()
                    .map_err(|err| refuse(err.to_string()))?;
                runtime.block_on(async {
                    let listener = TcpListener::bind((host.as_str(), port))
                        .await
                        .map_err(|err| refuse(err.to_string()))?;
                    serve(Arc::from(worker.as_str()), listener).await;
                    Ok(true)
                })
            })
            .join()
            .expect("the thread that serves the worker")
    })
}

async fn serve(worker: Arc<str>, listener: TcpListener) {
    loop {
        let Ok((stream, _)) = listener.accept().await else {
            tokio::time::sleep(ACCEPT_RETRY).await;
            continue;
        };
        let worker = worker.clone();
        tokio::spawn(async move {
            let service = service_fn(move |req| answer(worker.clone(), req));
            let _ = http1::Builder::new()
                .serve_connection(TokioIo::new(stream), service)
                .await;
        });
    }
}

async fn answer(
    worker: Arc<str>,
    req: Request<Incoming>,
) -> Result<Response<Full<Bytes>>, Infallible> {
    if req.method() != Method::POST {
        return Ok(respond(StatusCode::METHOD_NOT_ALLOWED.as_u16(), Vec::new()));
    }
    let body = match req.into_body().collect().await {
        Ok(collected) => collected.to_bytes(),
        Err(err) => {
            return Ok(respond(
                StatusCode::BAD_REQUEST.as_u16(),
                format!("the delivery could not be read: {err}").into_bytes(),
            ))
        }
    };
    let (status, body) = deliver(&worker, &body).await;
    Ok(respond(status, body))
}

fn respond(status: u16, body: Vec<u8>) -> Response<Full<Bytes>> {
    let content_type = match status {
        200 | 422 => "application/json",
        _ => "text/plain; charset=utf-8",
    };
    let mut response = Response::new(Full::new(Bytes::from(body)));
    *response.status_mut() =
        StatusCode::from_u16(status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        header::HeaderValue::from_static(content_type),
    );
    response
}
