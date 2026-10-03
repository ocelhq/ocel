use super::appsync::{build_appsync_publish_url, publish_to_appsync};
use super::token::{mint_token, Operation};
use super::Realtime;
use crate::proto::common::bindings::v1::{RealtimeProperties, RealtimeTransport};
use crate::Error;
use bytes::Bytes;
use futures_util::future::BoxFuture;
use http_body_util::{BodyExt, Full};
use hyper_util::rt::TokioIo;
use serde::Serialize;
use std::sync::{Arc, OnceLock};
use std::time::Duration;

const PUBLISH_TIMEOUT: Duration = Duration::from_secs(10);
const MAX_ANSWER_BYTES: usize = 64 << 10;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub(crate) enum Transport {
    AppsyncEvents,
    OcelGateway,
}

pub(crate) fn read_transport(properties: &RealtimeProperties) -> Option<Transport> {
    match properties.transport.as_known()? {
        RealtimeTransport::REALTIME_TRANSPORT_APPSYNC_EVENTS => Some(Transport::AppsyncEvents),
        RealtimeTransport::REALTIME_TRANSPORT_OCEL_GATEWAY => Some(Transport::OcelGateway),
        _ => None,
    }
}

fn build_gateway_publish_url(socket_url: &str) -> Option<http::Uri> {
    let uri: http::Uri = socket_url.parse().ok()?;
    let scheme = if uri.scheme_str() == Some("wss") {
        "https"
    } else {
        "http"
    };
    format!("{scheme}://{}/publish", uri.authority()?)
        .parse()
        .ok()
}

fn get_tls_connector() -> tokio_rustls::TlsConnector {
    static CONFIG: OnceLock<Arc<rustls::ClientConfig>> = OnceLock::new();
    let config = CONFIG.get_or_init(|| {
        let mut roots = rustls::RootCertStore::empty();
        roots.add_parsable_certificates(rustls_native_certs::load_native_certs().certs);
        Arc::new(
            rustls::ClientConfig::builder_with_provider(Arc::new(
                rustls::crypto::ring::default_provider(),
            ))
            .with_safe_default_protocol_versions()
            .expect("ring supports the default protocol versions")
            .with_root_certificates(roots)
            .with_no_client_auth(),
        )
    });
    tokio_rustls::TlsConnector::from(config.clone())
}

async fn send<S>(stream: S, request: http::Request<Full<Bytes>>) -> Result<(u16, Bytes), String>
where
    S: tokio::io::AsyncRead + tokio::io::AsyncWrite + Send + Unpin + 'static,
{
    let (mut sender, connection) = hyper::client::conn::http1::handshake(TokioIo::new(stream))
        .await
        .map_err(|err| err.to_string())?;
    tokio::spawn(connection);
    let response = sender
        .send_request(request)
        .await
        .map_err(|err| err.to_string())?;
    let status = response.status().as_u16();
    let mut body = response.into_body();
    let mut answer = Vec::new();
    while answer.len() < MAX_ANSWER_BYTES {
        let Some(frame) = body.frame().await else {
            break;
        };
        if let Ok(data) = frame.map_err(|err| err.to_string())?.into_data() {
            let kept = data.len().min(MAX_ANSWER_BYTES - answer.len());
            answer.extend_from_slice(&data[..kept]);
        }
    }
    Ok((status, Bytes::from(answer)))
}

pub(crate) async fn send_request(
    method: http::Method,
    uri: &http::Uri,
    headers: &[(String, String)],
    body: Bytes,
) -> Result<(u16, Bytes), String> {
    let host = uri.host().ok_or("the url names no host")?.to_string();
    let https = uri.scheme_str() == Some("https");
    let port = uri.port_u16().unwrap_or(if https { 443 } else { 80 });
    let mut request = http::Request::builder()
        .method(method)
        .uri(uri.path_and_query().map_or("/", |path| path.as_str()))
        .header(
            "host",
            uri.authority().map(ToString::to_string).unwrap_or_default(),
        )
        .header("content-length", body.len());
    for (name, value) in headers {
        request = request.header(name.as_str(), value.as_str());
    }
    let request = request
        .body(Full::new(body))
        .map_err(|err| err.to_string())?;
    let stream = tokio::net::TcpStream::connect((host.as_str(), port))
        .await
        .map_err(|err| err.to_string())?;
    if !https {
        return send(stream, request).await;
    }
    let name = rustls::pki_types::ServerName::try_from(host).map_err(|err| err.to_string())?;
    let stream = get_tls_connector()
        .connect(name, stream)
        .await
        .map_err(|err| err.to_string())?;
    send(stream, request).await
}

pub(crate) fn mint_publish_token(
    rt: &Realtime,
    properties: &RealtimeProperties,
    channel: &str,
) -> Result<String, String> {
    mint_token(rt, properties, "server", Operation::Publish, channel).map(|token| token.token)
}

async fn publish_to_gateway(
    url: &http::Uri,
    token: &str,
    channel: &str,
    envelope: Bytes,
) -> Result<(), String> {
    let headers = [
        ("authorization".to_string(), format!("Bearer {token}")),
        ("content-type".to_string(), "application/json".to_string()),
    ];
    let (status, _) = send_request(http::Method::POST, url, &headers, envelope).await?;
    if !(200..300).contains(&status) {
        return Err(format!(
            "the gateway refused a publish on {channel} with status {status}"
        ));
    }
    Ok(())
}

pub(crate) async fn publish(
    rt: &Realtime,
    properties: &RealtimeProperties,
    transport: Transport,
    channel: &str,
    token: &str,
    envelope: Bytes,
) -> Result<(), Error> {
    let failed = |said: String| Error::PublishFailed {
        name: rt.inner.name.clone(),
        said,
    };
    let (peer, sent): (&str, BoxFuture<'_, Result<(), String>>) = match transport {
        Transport::AppsyncEvents => {
            let endpoint = build_appsync_publish_url(&properties.host).ok_or_else(|| {
                failed(format!(
                    "the binding's host '{}' is no host",
                    properties.host
                ))
            })?;
            (
                "AppSync",
                Box::pin(async move {
                    publish_to_appsync(&endpoint, &properties.host, channel, envelope).await
                }),
            )
        }
        Transport::OcelGateway => {
            let url = build_gateway_publish_url(&properties.url).ok_or_else(|| {
                failed(format!("the binding's url '{}' is no URL", properties.url))
            })?;
            (
                "the gateway",
                Box::pin(async move { publish_to_gateway(&url, token, channel, envelope).await }),
            )
        }
    };
    tokio::time::timeout(PUBLISH_TIMEOUT, sent)
        .await
        .map_err(|_| {
            failed(format!(
                "{peer} did not answer a publish on {channel} within {}s",
                PUBLISH_TIMEOUT.as_secs()
            ))
        })?
        .map_err(|err| failed(format!("publish on {channel}: {err}")))
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    #[tokio::test]
    async fn an_answer_is_read_no_further_than_64_kib() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let host = listener.local_addr().unwrap();
        tokio::spawn(async move {
            let (mut stream, _) = listener.accept().await.unwrap();
            let mut request = [0u8; 4096];
            let _ = stream.read(&mut request).await;
            let answer = vec![b'x'; 4 << 20];
            let head = format!(
                "HTTP/1.1 200 OK\r\ncontent-length: {}\r\n\r\n",
                answer.len()
            );
            let _ = stream.write_all(head.as_bytes()).await;
            let _ = stream.write_all(&answer).await;
        });
        let uri: http::Uri = format!("http://{host}/creds").parse().unwrap();
        let (status, body) = send_request(http::Method::GET, &uri, &[], Bytes::new())
            .await
            .unwrap();
        assert_eq!(status, 200);
        assert_eq!(body.len(), 64 << 10);
    }
}
