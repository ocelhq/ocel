use super::token::{mint_tokens, MintedToken, Operation};
use super::wire::encode_wire_channel;
use super::{generate_envelope_id, DenialCode, ErasedAuth, Realtime, Request, ServedChannel};
use bytes::Bytes;
use futures_util::future::join_all;
use futures_util::FutureExt;
use serde::Serialize;
use serde_json::{Map, Value};
use std::any::Any;
use std::collections::BTreeMap;
use std::panic::AssertUnwindSafe;
use std::sync::Arc;

const MAX_OPERATIONS: usize = 50;
pub(crate) const MAX_REQUEST_BYTES: usize = 1 << 20;
const BATCH_KEYS: [&str; 2] = ["connect", "ops"];
const OPERATION_KEYS: [&str; 4] = ["op", "pattern", "params", "body"];

#[derive(Serialize)]
struct Grant {
    i: usize,
    wire: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    token: Option<String>,
}

#[derive(Serialize)]
struct DeniedOperation {
    i: usize,
    code: DenialCode,
}

#[derive(Serialize)]
struct Answer {
    url: String,
    host: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    connect: Option<MintedToken>,
    grants: Vec<Grant>,
    denied: Vec<DeniedOperation>,
}

enum OperationFailure {
    Denied(DenialCode),
    Failed(&'static str),
}

impl From<DenialCode> for OperationFailure {
    fn from(code: DenialCode) -> Self {
        Self::Denied(code)
    }
}

struct SubscribeOperation<'a> {
    channel: &'a ServedChannel,
    params: BTreeMap<String, String>,
    wire: String,
}

struct PublishOperation<'a> {
    channel: &'a ServedChannel,
    params: BTreeMap<String, String>,
    wire: String,
    body: Box<dyn Any + Send>,
    envelope: Bytes,
}

enum ReadOperation<'a> {
    Subscribe(SubscribeOperation<'a>),
    Publish(PublishOperation<'a>),
}

struct Caller {
    auth: Option<ErasedAuth>,
    subject: String,
}

fn respond(status: u16, body: Option<Value>, headers: &[(&str, String)]) -> http::Response<Bytes> {
    let mut response = http::Response::builder()
        .status(status)
        .header("cache-control", "no-store");
    for (name, value) in headers {
        response = response.header(*name, value);
    }
    let content = match body {
        Some(body) => {
            response = response.header("content-type", "application/json");
            Bytes::from(serde_json::to_vec(&body).unwrap_or_default())
        }
        None => Bytes::new(),
    };
    response
        .body(content)
        .expect("a response built from valid parts")
}

fn respond_with_error(
    status: u16,
    message: &str,
    headers: &[(&str, String)],
) -> http::Response<Bytes> {
    respond(
        status,
        Some(serde_json::json!({ "error": message })),
        headers,
    )
}

fn read_header<'a>(request: &'a Request, name: &str) -> Option<&'a str> {
    request
        .headers
        .get(name)
        .and_then(|value| value.to_str().ok())
}

fn read_first_forwarded<'a>(request: &'a Request, name: &str) -> Option<&'a str> {
    read_header(request, name)
        .and_then(|value| value.split(',').next())
        .map(str::trim)
        .filter(|value| !value.is_empty())
}

fn read_own_host(request: &Request) -> String {
    read_first_forwarded(request, "x-forwarded-host")
        .or_else(|| read_header(request, "host"))
        .map(str::to_string)
        .or_else(|| {
            request
                .uri
                .authority()
                .map(|authority| authority.to_string())
        })
        .unwrap_or_default()
}

fn read_own_scheme(request: &Request) -> String {
    read_first_forwarded(request, "x-forwarded-proto")
        .or_else(|| request.uri.scheme_str())
        .unwrap_or("http")
        .to_ascii_lowercase()
}

fn resolve_socket_url(url: &str, request: &Request) -> String {
    if !url.starts_with('/') {
        return url.to_string();
    }
    let scheme = if read_own_scheme(request) == "https" {
        "wss"
    } else {
        "ws"
    };
    format!("{scheme}://{}{url}", read_own_host(request))
}

fn is_same_origin(request: &Request, origin: &str) -> bool {
    let Ok(uri) = origin.parse::<http::Uri>() else {
        return false;
    };
    let (Some(scheme), Some(authority)) = (uri.scheme_str(), uri.authority()) else {
        return false;
    };
    !authority.host().is_empty()
        && scheme.eq_ignore_ascii_case(&read_own_scheme(request))
        && authority
            .as_str()
            .eq_ignore_ascii_case(&read_own_host(request))
}

fn parse_batch(body: &[u8]) -> Option<(bool, Vec<Value>)> {
    let Ok(Value::Object(mut members)) = serde_json::from_slice::<Value>(body) else {
        return None;
    };
    if members
        .keys()
        .any(|key| !BATCH_KEYS.contains(&key.as_str()))
    {
        return None;
    }
    let connect = match members.get("connect") {
        None => false,
        Some(Value::Bool(connect)) => *connect,
        Some(_) => return None,
    };
    match members.remove("ops") {
        Some(Value::Array(ops)) => Some((connect, ops)),
        _ => None,
    }
}

fn read_params(members: &Map<String, Value>) -> Result<BTreeMap<String, String>, DenialCode> {
    match members.get("params") {
        None | Some(Value::Null) => Ok(BTreeMap::new()),
        Some(Value::Object(params)) => params
            .iter()
            .map(|(name, value)| match value {
                Value::String(value) => Ok((name.clone(), value.clone())),
                _ => Err(DenialCode::InvalidParams),
            })
            .collect(),
        Some(_) => Err(DenialCode::InvalidParams),
    }
}

fn read_operation<'a>(rt: &'a Realtime, op: &Value) -> Result<ReadOperation<'a>, OperationFailure> {
    let Value::Object(members) = op else {
        return Err(DenialCode::InvalidOp.into());
    };
    if members
        .keys()
        .any(|key| !OPERATION_KEYS.contains(&key.as_str()))
    {
        return Err(DenialCode::InvalidOp.into());
    }
    let operation = match members.get("op").and_then(Value::as_str) {
        Some("subscribe") => Operation::Subscribe,
        Some("publish") => Operation::Publish,
        _ => return Err(DenialCode::UnknownOp.into()),
    };
    if operation == Operation::Subscribe && members.contains_key("body") {
        return Err(DenialCode::InvalidOp.into());
    }
    let channel = members
        .get("pattern")
        .and_then(Value::as_str)
        .and_then(|pattern| rt.inner.channels.get(pattern))
        .ok_or(DenialCode::UnknownPattern)?;
    let publish = match operation {
        Operation::Publish => Some(channel.publish.as_ref().ok_or(DenialCode::NoPublishRule)?),
        _ => None,
    };
    let params = read_params(members)?;
    let wildcard = operation == Operation::Subscribe && channel.wildcard;
    let wire = encode_wire_channel(&rt.inner.name, &channel.pattern, &params, wildcard)?;
    let Some((_, decode)) = publish else {
        return Ok(ReadOperation::Subscribe(SubscribeOperation {
            channel,
            params,
            wire,
        }));
    };
    let raw = members.get("body").ok_or(DenialCode::InvalidBody)?;
    let (body, data) = decode(raw).map_err(|_| DenialCode::InvalidBody)?;
    let id = generate_envelope_id().map_err(|_| OperationFailure::Failed("an event id failed"))?;
    let envelope = channel.encode_envelope(&wire, data, id)?;
    Ok(ReadOperation::Publish(PublishOperation {
        channel,
        params,
        wire,
        body,
        envelope,
    }))
}

async fn decide(
    rule: &super::RuleFn,
    caller: &Caller,
    params: BTreeMap<String, String>,
    body: Option<Box<dyn Any + Send>>,
    request: &Arc<Request>,
) -> Result<(), DenialCode> {
    let auth = caller.auth.clone().ok_or(DenialCode::Unauthenticated)?;
    match rule(auth, params, body, request.clone()).await {
        Ok(true) => Ok(()),
        Ok(false) => Err(DenialCode::Forbidden),
        Err(_) => Err(DenialCode::RuleError),
    }
}

async fn authorize_subscribe(
    request: &Arc<Request>,
    caller: &Caller,
    subscribe: SubscribeOperation<'_>,
) -> Result<String, DenialCode> {
    if let Some(rule) = &subscribe.channel.subscribe {
        decide(rule, caller, subscribe.params, None, request).await?;
    }
    Ok(subscribe.wire)
}

async fn serve_publish(
    rt: &Realtime,
    request: &Arc<Request>,
    caller: &Caller,
    publish: PublishOperation<'_>,
) -> Result<String, DenialCode> {
    let (rule, _) = publish
        .channel
        .publish
        .as_ref()
        .expect("a publish op was read only for a channel with a publish rule");
    decide(rule, caller, publish.params, Some(publish.body), request).await?;
    rt.publish_event(&publish.wire, publish.envelope)
        .await
        .map_err(|_| DenialCode::PublishFailed)?;
    Ok(publish.wire)
}

async fn serve_batch(
    rt: &Realtime,
    request: Request,
    connect: bool,
    ops: Vec<Value>,
) -> Result<Answer, &'static str> {
    let properties = rt
        .read_properties("handle")
        .map_err(|_| "the realtime resource has no binding")?;
    let request = Arc::new(request);
    let authorized = match &rt.inner.authorize {
        Some(authorize) => authorize(request.clone())
            .await
            .map_err(|_| "authorize failed")?,
        None => None,
    };
    let caller = match authorized {
        Some((auth, subject)) => Caller {
            auth: Some(auth),
            subject,
        },
        None => Caller {
            auth: None,
            subject: "anonymous".to_string(),
        },
    };
    let (mut grants, mut denied) = (Vec::new(), Vec::new());
    let (mut subscribes, mut publishes) = (Vec::new(), Vec::new());
    for (i, op) in ops.iter().enumerate() {
        match read_operation(rt, op) {
            Ok(ReadOperation::Subscribe(subscribe)) => subscribes.push((i, subscribe)),
            Ok(ReadOperation::Publish(publish)) => publishes.push((i, publish)),
            Err(OperationFailure::Denied(code)) => denied.push(DeniedOperation { i, code }),
            Err(OperationFailure::Failed(message)) => return Err(message),
        }
    }
    let subscribed = join_all(subscribes.into_iter().map(|(i, subscribe)| {
        let (request, caller) = (&request, &caller);
        async move { (i, authorize_subscribe(request, caller, subscribe).await) }
    }))
    .await;
    for (i, outcome) in subscribed {
        match outcome {
            Ok(wire) => grants.push(Grant {
                i,
                wire,
                token: None,
            }),
            Err(code) => denied.push(DeniedOperation { i, code }),
        }
    }
    let connect = mint_answer_tokens(rt, &caller.subject, connect, &mut grants).await?;
    for (i, publish) in publishes {
        match serve_publish(rt, &request, &caller, publish).await {
            Ok(wire) => grants.push(Grant {
                i,
                wire,
                token: None,
            }),
            Err(code) => denied.push(DeniedOperation { i, code }),
        }
    }
    grants.sort_by_key(|grant| grant.i);
    denied.sort_by_key(|denial| denial.i);
    Ok(Answer {
        url: resolve_socket_url(&properties.url, &request),
        host: properties.host.clone(),
        connect,
        grants,
        denied,
    })
}

async fn mint_answer_tokens(
    rt: &Realtime,
    subject: &str,
    connect: bool,
    subscribe_grants: &mut [Grant],
) -> Result<Option<MintedToken>, &'static str> {
    if !connect && subscribe_grants.is_empty() {
        return Ok(None);
    }
    let channels = subscribe_grants
        .iter()
        .map(|grant| grant.wire.clone())
        .collect();
    let minted = mint_tokens(rt, subject, connect, channels)
        .await
        .map_err(|_| "minting a token failed")?;
    for (grant, token) in subscribe_grants.iter_mut().zip(minted.subscribe_tokens) {
        grant.wire = token.channel;
        grant.token = Some(token.token);
    }
    Ok(minted.connect_token.map(|token| MintedToken {
        token,
        expires_at: minted.expires_at,
    }))
}

pub(crate) async fn handle(
    rt: &Realtime,
    request: Request,
    is_body_too_large: bool,
) -> http::Response<Bytes> {
    let origin = read_header(&request, "origin").map(str::to_string);
    let allowed_origin = origin
        .as_ref()
        .filter(|origin| rt.inner.allowed_origins.contains(origin));
    if request.method == http::Method::OPTIONS {
        if let Some(origin) = allowed_origin {
            return respond(
                204,
                None,
                &[
                    ("access-control-allow-origin", origin.clone()),
                    ("access-control-allow-methods", "POST".to_string()),
                    (
                        "access-control-allow-headers",
                        "authorization, content-type".to_string(),
                    ),
                    ("access-control-max-age", "600".to_string()),
                    ("vary", "Origin".to_string()),
                ],
            );
        }
    }
    if request.method != http::Method::POST {
        return respond_with_error(
            405,
            "the realtime handler takes POST",
            &[("allow", "POST".to_string())],
        );
    }
    let cors: Vec<(&str, String)> = match (&origin, allowed_origin) {
        (_, Some(origin)) => vec![
            ("access-control-allow-origin", origin.clone()),
            ("vary", "Origin".to_string()),
        ],
        (Some(origin), None) if !is_same_origin(&request, origin) => {
            return respond_with_error(403, "this origin may not call the realtime handler", &[]);
        }
        _ => Vec::new(),
    };
    let media_type = read_header(&request, "content-type")
        .and_then(|value| value.split(';').next())
        .map(|value| value.trim().to_ascii_lowercase());
    if media_type.as_deref() != Some("application/json") {
        return respond_with_error(415, "the realtime handler takes application/json", &cors);
    }
    if is_body_too_large || request.body.len() > MAX_REQUEST_BYTES {
        return respond_with_error(413, "a request is at most 1048576 bytes", &cors);
    }
    let Some((connect, ops)) = parse_batch(&request.body) else {
        return respond_with_error(
            400,
            "the body is exactly { connect?: boolean, ops: [{ op, pattern, params?, body? }] }",
            &cors,
        );
    };
    if ops.len() > MAX_OPERATIONS {
        return respond_with_error(400, "a request holds at most 50 ops", &cors);
    }
    match AssertUnwindSafe(serve_batch(rt, request, connect, ops))
        .catch_unwind()
        .await
    {
        Ok(Ok(answer)) => respond(200, serde_json::to_value(answer).ok(), &cors),
        Ok(Err(message)) => respond_with_error(500, message, &cors),
        Err(_) => respond_with_error(500, "the realtime handler failed", &cors),
    }
}
