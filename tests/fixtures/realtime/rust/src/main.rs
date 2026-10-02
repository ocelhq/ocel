#![allow(non_snake_case)]

use std::borrow::Cow;
use std::collections::BTreeMap;
use std::convert::Infallible;
use std::path::Path;
use std::sync::Arc;

use axum::extract::State;
use axum::http::{HeaderMap, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use base64::engine::general_purpose::{STANDARD, URL_SAFE_NO_PAD};
use base64::Engine;
use ocel::realtime::{Channel, PublishContext, Realtime, Request, SubscribeContext};
use ring::rand::SystemRandom;
use ring::signature::Ed25519KeyPair;
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use serde_json::{json, Value};

const DEFAULT_PORT: &str = "3116";
const BINDING_KEY: &str = "OCEL_RESOURCE_REALTIME_app";

#[derive(ocel::Env)]
struct Env {
    journey_nonce: ocel::Secret,
}

#[derive(Serialize)]
struct Caller {
    id: String,
    orders: Vec<String>,
    projects: Vec<String>,
    may_publish: bool,
}

#[derive(Serialize, Deserialize, schemars::JsonSchema)]
struct OrderEvent {
    status: String,
}

#[derive(Serialize, Deserialize, schemars::JsonSchema)]
struct DeployEvent {
    state: String,
}

#[derive(Serialize, Deserialize)]
#[serde(transparent)]
struct Note(Value);

impl schemars::JsonSchema for Note {
    fn schema_name() -> Cow<'static, str> {
        "Note".into()
    }

    fn json_schema(_: &mut schemars::SchemaGenerator) -> schemars::Schema {
        schemars::json_schema!({
            "type": "object",
            "properties": { "text": { "type": "string" } },
            "required": ["text"],
            "additionalProperties": false
        })
    }
}

#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "orders/:orderId", event = OrderEvent, schema)]
struct Orders {
    orderId: String,
}

#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "projects/:projectId/deploys/:deployId", event = DeployEvent, schema, wildcard)]
struct Deploys {
    projectId: String,
    deployId: String,
}

#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "rooms/:roomId", event = Note, schema, publish)]
struct Rooms {
    roomId: String,
}

#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "status", event = Note, schema, public)]
struct Status;

fn read_caller(request: &Request) -> Option<Caller> {
    let credential = request
        .headers
        .get("authorization")?
        .to_str()
        .ok()?
        .strip_prefix("Bearer ")?;
    let claims: BTreeMap<String, String> = form_urlencoded::parse(credential.as_bytes())
        .into_owned()
        .collect();
    let list = |name: &str| -> Vec<String> {
        claims
            .get(name)
            .filter(|value| !value.is_empty())
            .map(|value| value.split(',').map(String::from).collect())
            .unwrap_or_default()
    };
    let id = claims.get("user").filter(|user| !user.is_empty())?.clone();
    Some(Caller {
        id,
        orders: list("orders"),
        projects: list("projects"),
        may_publish: claims.get("publish").is_some_and(|value| value == "yes"),
    })
}

fn build_realtime() -> Realtime {
    Realtime::builder("app")
        .authorize(
            |request: Arc<Request>| async move { Ok::<_, Infallible>(read_caller(&request)) },
        )
        .subscribe::<Orders>(|ctx: SubscribeContext<Caller, Orders>| async move {
            Ok::<_, Infallible>(ctx.auth.orders.contains(&ctx.params.orderId))
        })
        .subscribe::<Deploys>(|ctx: SubscribeContext<Caller, Deploys>| async move {
            let project = &ctx.params.projectId;
            Ok::<_, Infallible>(!project.is_empty() && ctx.auth.projects.contains(project))
        })
        .subscribe::<Rooms>(|_: SubscribeContext<Caller, Rooms>| async {
            Ok::<_, Infallible>(true)
        })
        .publish::<Rooms>(|ctx: PublishContext<Caller, Rooms>| async move {
            Ok::<_, Infallible>(ctx.auth.may_publish)
        })
        .build()
        .expect("the realtime resource this app declares")
}

#[derive(Clone)]
struct App {
    realtime: Realtime,
    env: Arc<Env>,
}

#[derive(Deserialize)]
struct PublishRequest {
    pattern: String,
    #[serde(default)]
    params: BTreeMap<String, String>,
    body: Value,
}

fn answer(status: StatusCode, body: Value) -> Response {
    (status, Json(body)).into_response()
}

async fn publish_on<C: Channel>(realtime: &Realtime, params: C, body: Value) -> Response {
    let Ok(event) = serde_json::from_value::<C::Event>(body) else {
        return answer(
            StatusCode::BAD_REQUEST,
            json!({ "error": format!("no event of channel {}", C::PATTERN) }),
        );
    };
    match realtime.publish(&params, &event).await {
        Ok(()) => StatusCode::NO_CONTENT.into_response(),
        Err(ocel::Error::PublishRefused { code, .. }) => {
            answer(StatusCode::UNPROCESSABLE_ENTITY, json!({ "code": code }))
        }
        Err(err) => answer(StatusCode::BAD_GATEWAY, json!({ "error": err.to_string() })),
    }
}

async fn publish(State(app): State<App>, Json(sent): Json<PublishRequest>) -> Response {
    let param = |name: &str| sent.params.get(name).cloned().unwrap_or_default();
    let rt = &app.realtime;
    match sent.pattern.as_str() {
        Orders::PATTERN => {
            let params = Orders {
                orderId: param("orderId"),
            };
            publish_on(rt, params, sent.body).await
        }
        Deploys::PATTERN => {
            let params = Deploys {
                projectId: param("projectId"),
                deployId: param("deployId"),
            };
            publish_on(rt, params, sent.body).await
        }
        Rooms::PATTERN => {
            publish_on(
                rt,
                Rooms {
                    roomId: param("roomId"),
                },
                sent.body,
            )
            .await
        }
        Status::PATTERN => publish_on(rt, Status, sent.body).await,
        other => answer(
            StatusCode::BAD_REQUEST,
            json!({ "error": format!("no channel of pattern {other} is declared") }),
        ),
    }
}

fn is_same_secret(given: &[u8], expected: &[u8]) -> bool {
    given.len() == expected.len()
        && given
            .iter()
            .zip(expected)
            .fold(0u8, |diff, (a, b)| diff | (a ^ b))
            == 0
}

fn is_journey_nonce(app: &App, headers: &HeaderMap) -> bool {
    let Some(given) = headers.get("x-journey-nonce") else {
        return false;
    };
    let Ok(expected) = app.env.journey_nonce.value() else {
        return false;
    };
    !given.is_empty() && is_same_secret(given.as_bytes(), expected.as_bytes())
}

fn read_binding() -> Result<String, String> {
    if let Ok(dir) = std::env::var("OCEL_LIVE_DIR") {
        if let Ok(raw) = std::fs::read_to_string(Path::new(&dir).join(BINDING_KEY)) {
            return Ok(raw);
        }
    }
    std::env::var(BINDING_KEY).map_err(|_| format!("{BINDING_KEY} was not delivered"))
}

fn read_signing_key(signed_by: &str) -> Result<Option<Ed25519KeyPair>, String> {
    match signed_by {
        "binding" => {
            let binding: Value =
                serde_json::from_str(&read_binding()?).map_err(|err| err.to_string())?;
            let seed = binding["realtime"]["signingKey"]
                .as_str()
                .ok_or("the binding carries no signing key")?;
            let seed = STANDARD.decode(seed).map_err(|err| err.to_string())?;
            Ed25519KeyPair::from_seed_unchecked(&seed)
                .map(Some)
                .map_err(|err| err.to_string())
        }
        "another-key" => {
            let pkcs8 = Ed25519KeyPair::generate_pkcs8(&SystemRandom::new())
                .map_err(|err| err.to_string())?;
            Ed25519KeyPair::from_pkcs8(pkcs8.as_ref())
                .map(Some)
                .map_err(|err| err.to_string())
        }
        "nobody" => Ok(None),
        other => Err(format!("no signer named {other:?}")),
    }
}

#[derive(Deserialize)]
struct SignRequest {
    header: Box<RawValue>,
    claims: Box<RawValue>,
    #[serde(rename = "signedBy")]
    signed_by: String,
}

async fn sign_token(
    State(app): State<App>,
    headers: HeaderMap,
    Json(sent): Json<SignRequest>,
) -> Response {
    if !is_journey_nonce(&app, &headers) {
        return answer(
            StatusCode::FORBIDDEN,
            json!({ "error": "signing a token needs the nonce the harness set" }),
        );
    }
    let input = format!(
        "{}.{}",
        URL_SAFE_NO_PAD.encode(sent.header.get()),
        URL_SAFE_NO_PAD.encode(sent.claims.get())
    );
    let key = match read_signing_key(&sent.signed_by) {
        Ok(key) => key,
        Err(err) => return answer(StatusCode::INTERNAL_SERVER_ERROR, json!({ "error": err })),
    };
    let signature = key
        .map(|key| URL_SAFE_NO_PAD.encode(key.sign(input.as_bytes()).as_ref()))
        .unwrap_or_default();
    answer(
        StatusCode::OK,
        json!({ "token": format!("{input}.{signature}") }),
    )
}

#[ocel::main]
#[tokio::main]
async fn main() {
    let env = Env::load().expect("the environment this app declares");
    let realtime = build_realtime();
    let app = Router::new()
        .route(
            "/health",
            get(|| async { Json(json!({ "ok": true, "app": "web" })) }),
        )
        .nest_service(
            "/api/realtime",
            ocel::realtime::axum::router(realtime.clone()),
        )
        .route("/api/publish", post(publish))
        .route("/api/tokens", post(sign_token))
        .with_state(App {
            realtime,
            env: Arc::new(env),
        });

    let port = std::env::var("PORT").unwrap_or_else(|_| DEFAULT_PORT.to_string());
    let listener = tokio::net::TcpListener::bind(format!("0.0.0.0:{port}"))
        .await
        .expect("bind");
    println!("realtime fixture listening on http://localhost:{port}");
    axum::serve(listener, app).await.expect("serve");
}
