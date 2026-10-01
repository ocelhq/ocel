#![cfg(feature = "realtime")]

mod realtime;

use ocel::realtime::{DenialCode, Realtime};
use ocel::realtime::{PublishRule, Request, SubscribeContext, SubscribeRule};
use realtime::{call, post, read_claims, read_fixture, FakeGateway, StalledGateway};
use serde_json::{json, Value};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::sync::MutexGuard;

#[derive(serde::Serialize, serde::Deserialize)]
struct OrderEvent {
    status: String,
}

#[derive(serde::Serialize, serde::Deserialize, Clone, Debug, PartialEq)]
#[serde(deny_unknown_fields)]
struct ChatMessage {
    text: String,
}

#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "orders/:order_id", event = OrderEvent, token_ttl = "30s")]
struct Orders {
    order_id: String,
}

#[allow(dead_code)]
#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "projects/:project_id/deploys/:deploy_id", event = OrderEvent, wildcard)]
struct Deploys {
    project_id: String,
    deploy_id: String,
}

#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "rooms/:room_id", event = ChatMessage, publish)]
struct Rooms {
    room_id: String,
}

#[allow(dead_code)]
#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "status", event = OrderEvent, public)]
struct Status;

#[allow(dead_code)]
#[derive(ocel::Channel)]
#[ocel(realtime = "chat", pattern = "orders/:order_id", event = OrderEvent, public)]
struct ChatOrders {
    order_id: String,
}

#[derive(serde::Serialize)]
struct Caller {
    id: String,
}

#[derive(Default)]
struct Seen {
    subscribes: Vec<(String, String, Option<String>)>,
    publishes: Vec<(String, String, ChatMessage)>,
    fail_next_subscribe: bool,
}

static ENV: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());

async fn deliver(binding: &str) -> MutexGuard<'static, ()> {
    let guard = ENV.lock().await;
    std::env::remove_var("OCEL_PHASE");
    std::env::set_var("OCEL_RESOURCE_REALTIME_app", binding);
    guard
}

fn build(seen: Arc<Mutex<Seen>>) -> Realtime {
    let (subscribes, publishes) = (seen.clone(), seen);
    Realtime::builder("app")
        .authorize(|request| async move {
            let user = request
                .headers
                .get("x-user")
                .and_then(|value| value.to_str().ok());
            Ok::<_, String>(user.map(|user| Caller {
                id: user.to_string(),
            }))
        })
        .allow_origins(["https://app.example"])
        .subscribe::<Orders>(move |ctx| {
            let seen = subscribes.clone();
            async move {
                let mut seen = seen.lock().unwrap();
                let header = ctx
                    .request
                    .headers
                    .get("x-user")
                    .map(|value| value.to_str().unwrap().to_string());
                seen.subscribes
                    .push((ctx.auth.id.clone(), ctx.params.order_id.clone(), header));
                if std::mem::take(&mut seen.fail_next_subscribe) {
                    return Err("db down".to_string());
                }
                Ok(ctx.params.order_id == "o-1" && ctx.auth.id == "u1")
            }
        })
        .subscribe::<Deploys>(|_| async { Ok::<_, String>(true) })
        .subscribe::<Rooms>(|_| async { Ok::<_, String>(true) })
        .publish::<Rooms>(move |ctx| {
            let seen = publishes.clone();
            async move {
                seen.lock().unwrap().publishes.push((
                    ctx.auth.id.clone(),
                    ctx.params.room_id.clone(),
                    ctx.body.clone(),
                ));
                Ok::<_, String>(ctx.auth.id == "u1")
            }
        })
        .build()
        .expect("the realtime resource builds")
}

fn build_ruled(
    orders: impl SubscribeRule<Caller, Orders>,
    rooms: impl PublishRule<Caller, Rooms>,
) -> Realtime {
    Realtime::builder("app")
        .authorize(|request| async move {
            let user = request
                .headers
                .get("x-user")
                .and_then(|value| value.to_str().ok());
            Ok::<_, String>(user.map(|user| Caller {
                id: user.to_string(),
            }))
        })
        .subscribe::<Orders>(orders)
        .subscribe::<Deploys>(|_| async { Ok::<_, String>(true) })
        .subscribe::<Rooms>(|_| async { Ok::<_, String>(true) })
        .publish::<Rooms>(rooms)
        .build()
        .expect("the realtime resource builds")
}

fn deliver_binding_with(field: &str, value: Value) -> String {
    let mut binding = read_fixture();
    binding["realtime"][field] = value;
    binding.to_string()
}

async fn deliver_appsync() -> MutexGuard<'static, ()> {
    deliver(&read_fixture().to_string()).await
}

#[tokio::test]
async fn the_handler_names_the_transport_and_mints_a_connect_token_when_asked() {
    let _env = deliver_appsync().await;
    let rt = build(Arc::default());

    let res = post(&rt, json!({ "connect": true, "ops": [] }), &[]).await;

    let fixture = read_fixture();
    assert_eq!(res["transport"], "appsync-events");
    assert_eq!(res["url"], fixture["realtime"]["url"]);
    assert_eq!(res["host"], fixture["realtime"]["host"]);
    let claims = read_claims(res["connect"]["token"].as_str().unwrap());
    assert_eq!(claims["iss"], "ocel:rt:app");
    assert_eq!(claims["aud"], fixture["realtime"]["host"]);
    assert_eq!(claims["sub"], "anonymous");
    assert_eq!(
        claims["ocel"],
        json!({ "op": "connect", "ch": "/app", "ns": "app" })
    );
    assert_eq!(claims["exp"], res["connect"]["expiresAt"]);
    assert_eq!(
        claims["exp"].as_u64().unwrap() - claims["iat"].as_u64().unwrap(),
        30
    );
}

#[tokio::test]
async fn the_handler_grants_a_public_subscribe_to_anyone() {
    let _env = deliver_appsync().await;
    let rt = build(Arc::default());

    let res = post(
        &rt,
        json!({ "ops": [{ "op": "subscribe", "pattern": "status", "params": {} }] }),
        &[],
    )
    .await;

    assert!(res.get("connect").is_none());
    assert_eq!(res["denied"], json!([]));
    assert_eq!(res["grants"][0]["wire"], "/app/status");
    let claims = read_claims(res["grants"][0]["token"].as_str().unwrap());
    assert_eq!(claims["sub"], "anonymous");
    assert_eq!(
        claims["ocel"],
        json!({ "op": "subscribe", "ch": "/app/status", "ns": "app" })
    );
}

#[tokio::test]
async fn the_handler_runs_the_subscribe_rule_with_the_auth_params_and_request() {
    let _env = deliver_appsync().await;
    let seen = Arc::new(Mutex::new(Seen::default()));
    let rt = build(seen.clone());

    let res = post(
        &rt,
        json!({ "connect": true, "ops": [
            { "op": "subscribe", "pattern": "orders/:order_id", "params": { "order_id": "o-1" } },
            { "op": "subscribe", "pattern": "orders/:order_id", "params": { "order_id": "o-2" } },
        ] }),
        &[("x-user", "u1")],
    )
    .await;

    assert_eq!(res["grants"].as_array().unwrap().len(), 1);
    assert_eq!(
        (
            res["grants"][0]["i"].clone(),
            res["grants"][0]["wire"].clone()
        ),
        (json!(0), json!("/app/orders/o-1"))
    );
    assert_eq!(res["denied"], json!([{ "i": 1, "code": "forbidden" }]));
    assert_eq!(
        read_claims(res["connect"]["token"].as_str().unwrap())["sub"],
        "u1"
    );
    assert_eq!(
        read_claims(res["grants"][0]["token"].as_str().unwrap())["sub"],
        "u1"
    );
    assert_eq!(
        seen.lock().unwrap().subscribes[0],
        ("u1".to_string(), "o-1".to_string(), Some("u1".to_string()))
    );
}

#[tokio::test]
async fn the_handler_denies_a_ruled_op_to_nobody_without_running_the_rule() {
    let _env = deliver_appsync().await;
    let seen = Arc::new(Mutex::new(Seen::default()));
    let rt = build(seen.clone());

    let res = post(
        &rt,
        json!({ "ops": [{ "op": "subscribe", "pattern": "orders/:order_id", "params": { "order_id": "o-1" } }] }),
        &[],
    )
    .await;

    assert_eq!(
        res["denied"],
        json!([{ "i": 0, "code": "unauthenticated" }])
    );
    assert!(seen.lock().unwrap().subscribes.is_empty());
}

#[tokio::test]
async fn the_handler_grants_a_wildcard_subscribe_as_the_prefix_and_star() {
    let _env = deliver_appsync().await;
    let rt = build(Arc::default());

    let res = post(
        &rt,
        json!({ "ops": [{ "op": "subscribe", "pattern": "projects/:project_id/deploys/:deploy_id", "params": { "project_id": "p_1" } }] }),
        &[("x-user", "u1")],
    )
    .await;

    assert_eq!(res["grants"][0]["wire"], "/app/projects/0zobptc/deploys/*");
}

#[tokio::test]
async fn the_handler_denies_each_op_it_cannot_serve_with_its_code() {
    let _env = deliver_appsync().await;
    let rt = build(Arc::default());

    let res = post(
        &rt,
        json!({ "ops": [
            { "op": "subscribe", "pattern": "nope", "params": {} },
            { "op": "subscribe", "pattern": "orders/:order_id", "params": {} },
            { "op": "subscribe", "pattern": "orders/:order_id", "params": { "order_id": 7 } },
            { "op": "subscribe", "pattern": "orders/:order_id", "params": { "order_id": "x".repeat(31) } },
            { "op": "unsubscribe", "pattern": "status", "params": {} },
            { "op": "publish", "pattern": "status", "params": {}, "body": { "status": "up" } },
        ] }),
        &[("x-user", "u1")],
    )
    .await;

    assert_eq!(res["grants"], json!([]));
    assert_eq!(
        res["denied"],
        json!([
            { "i": 0, "code": "unknown-pattern" },
            { "i": 1, "code": "missing-param" },
            { "i": 2, "code": "invalid-params" },
            { "i": 3, "code": "value-too-long" },
            { "i": 4, "code": "unknown-op" },
            { "i": 5, "code": "no-publish-rule" },
        ])
    );
}

#[tokio::test]
async fn the_handler_denies_an_op_whose_rule_fails_and_serves_the_rest() {
    let _env = deliver_appsync().await;
    let seen = Arc::new(Mutex::new(Seen {
        fail_next_subscribe: true,
        ..Seen::default()
    }));
    let rt = build(seen);

    let res = post(
        &rt,
        json!({ "ops": [
            { "op": "subscribe", "pattern": "orders/:order_id", "params": { "order_id": "o-1" } },
            { "op": "subscribe", "pattern": "status", "params": {} },
        ] }),
        &[("x-user", "u1")],
    )
    .await;

    assert_eq!(res["denied"], json!([{ "i": 0, "code": "rule-error" }]));
    assert_eq!(res["grants"][0]["i"], 1);
}

#[tokio::test]
async fn the_handler_answers_uncacheable_and_sets_no_cookie() {
    let _env = deliver_appsync().await;
    let rt = build(Arc::default());

    let answered = call(&rt, "POST", &[], r#"{"connect":true,"ops":[]}"#).await;

    assert_eq!(answered.headers["cache-control"], "no-store");
    assert!(answered.headers.get("set-cookie").is_none());
}

#[tokio::test]
async fn the_handler_refuses_what_is_no_batch_of_json() {
    let _env = deliver_appsync().await;
    let rt = build(Arc::default());

    let get = call(&rt, "GET", &[], "").await;
    assert_eq!(
        (get.status, get.headers["allow"].to_str().unwrap()),
        (405, "POST")
    );
    assert_eq!(get.headers["cache-control"], "no-store");
    assert_eq!(
        call(&rt, "POST", &[("content-type", "text/plain")], "{}")
            .await
            .status,
        415
    );
    for body in ["{", r#"{"ops":"x"}"#, "{}"] {
        assert_eq!(call(&rt, "POST", &[], body).await.status, 400, "{body}");
    }
    let ops: Vec<Value> = (0..51)
        .map(|_| json!({ "op": "subscribe", "pattern": "status" }))
        .collect();
    assert_eq!(
        call(&rt, "POST", &[], &json!({ "ops": ops }).to_string())
            .await
            .status,
        400
    );
}

#[tokio::test]
async fn the_handler_serves_its_own_and_allowed_origins_and_refuses_another() {
    let _env = deliver_appsync().await;
    let rt = build(Arc::default());
    let body = r#"{"ops":[]}"#;

    let same = call(&rt, "POST", &[("origin", "http://shop.example")], body).await;
    let other = call(&rt, "POST", &[("origin", "http://evil.example")], body).await;
    let other_scheme = call(&rt, "POST", &[("origin", "https://shop.example")], body).await;
    let opaque = call(&rt, "POST", &[("origin", "null")], body).await;
    let proxied = call(
        &rt,
        "POST",
        &[
            ("origin", "https://shop.example"),
            ("host", "10.0.0.5:3000"),
            ("x-forwarded-host", "shop.example"),
            ("x-forwarded-proto", "HTTPS, http"),
        ],
        body,
    )
    .await;
    let allowed = call(&rt, "POST", &[("origin", "https://app.example")], body).await;
    let preflight = call(&rt, "OPTIONS", &[("origin", "https://app.example")], "").await;
    let refused_preflight = call(&rt, "OPTIONS", &[("origin", "https://evil.example")], "").await;

    assert_eq!(same.status, 200);
    assert!(same.headers.get("access-control-allow-origin").is_none());
    assert_eq!(other.status, 403);
    assert!(other.headers.get("access-control-allow-origin").is_none());
    assert_eq!(other.headers["cache-control"], "no-store");
    assert_eq!(other_scheme.status, 403);
    assert_eq!(opaque.status, 403);
    assert_eq!(proxied.status, 200);
    assert_eq!(allowed.status, 200);
    assert_eq!(
        allowed.headers["access-control-allow-origin"],
        "https://app.example"
    );
    assert_eq!(allowed.headers["vary"], "Origin");
    assert_eq!(preflight.status, 204);
    assert_eq!(preflight.headers["access-control-allow-methods"], "POST");
    assert_eq!(
        preflight.headers["access-control-allow-headers"],
        "authorization, content-type"
    );
    assert_eq!(refused_preflight.status, 405);
    assert!(refused_preflight
        .headers
        .get("access-control-allow-origin")
        .is_none());
}

#[tokio::test]
async fn the_handler_answers_500_without_the_cause_when_authorize_fails() {
    let _env = deliver_appsync().await;
    let rt = Realtime::builder("app")
        .authorize(|_| async { Err::<Option<Caller>, _>("secret detail") })
        .subscribe::<Orders>(|_| async { Ok::<_, String>(true) })
        .subscribe::<Deploys>(|_| async { Ok::<_, String>(true) })
        .subscribe::<Rooms>(|_| async { Ok::<_, String>(true) })
        .publish::<Rooms>(|_| async { Ok::<_, String>(true) })
        .build()
        .unwrap();

    let answered = call(&rt, "POST", &[], r#"{"ops":[]}"#).await;

    assert_eq!(answered.status, 500);
    assert!(!answered.body.to_string().contains("secret detail"));
}

#[tokio::test]
async fn a_relayed_publish_runs_the_publish_rule_then_publishes_from_the_server() {
    let gateway = FakeGateway::serve();
    let _env = deliver(&gateway.binding()).await;
    let seen = Arc::new(Mutex::new(Seen::default()));
    let rt = build(seen.clone());

    let res = post(
        &rt,
        json!({ "ops": [{ "op": "publish", "pattern": "rooms/:room_id", "params": { "room_id": "r1" }, "body": { "text": "hi" } }] }),
        &[("x-user", "u1")],
    )
    .await;

    assert_eq!(res["transport"], "ocel-gateway");
    assert!(res.get("host").is_none());
    assert_eq!(res["grants"], json!([{ "i": 0, "wire": "/app/rooms/r1" }]));
    assert_eq!(
        seen.lock().unwrap().publishes[0],
        (
            "u1".to_string(),
            "r1".to_string(),
            ChatMessage {
                text: "hi".to_string()
            }
        )
    );
    let published = gateway.published.lock().unwrap();
    assert_eq!(published.len(), 1);
    assert_eq!(published[0].envelope["ch"], "/app/rooms/r1");
    assert_eq!(published[0].envelope["kind"], "live");
    assert_eq!(published[0].envelope["data"], json!({ "text": "hi" }));
}

#[tokio::test]
async fn a_relayed_publish_is_denied_when_its_rule_its_body_or_the_transport_refuses_it() {
    let gateway = FakeGateway::serve();
    let _env = deliver(&gateway.binding()).await;
    let rt = build(Arc::default());
    let publish = |text: Value| json!({ "ops": [{ "op": "publish", "pattern": "rooms/:room_id", "params": { "room_id": "r1" }, "body": { "text": text } }] });

    let invalid = post(&rt, publish(json!(1)), &[("x-user", "u1")]).await;
    let forbidden = post(&rt, publish(json!("x")), &[("x-user", "u2")]).await;
    assert!(gateway.published.lock().unwrap().is_empty());
    *gateway.status.lock().unwrap() = 401;
    let refused = post(&rt, publish(json!("hi")), &[("x-user", "u1")]).await;

    assert_eq!(
        invalid["denied"],
        json!([{ "i": 0, "code": "invalid-body" }])
    );
    assert_eq!(
        forbidden["denied"],
        json!([{ "i": 0, "code": "forbidden" }])
    );
    assert_eq!(
        refused["denied"],
        json!([{ "i": 0, "code": "publish-failed" }])
    );
}

#[tokio::test]
async fn publish_posts_the_envelope_to_the_gateway_with_a_publish_token() {
    let gateway = FakeGateway::serve();
    let _env = deliver(&gateway.binding()).await;
    let rt = build(Arc::default());

    rt.publish(
        &Orders {
            order_id: "o_1".to_string(),
        },
        &OrderEvent {
            status: "shipped".to_string(),
        },
    )
    .await
    .expect("published");

    let published = gateway.published.lock().unwrap();
    assert_eq!(published[0].path, "/publish");
    let envelope = &published[0].envelope;
    assert_eq!(
        (
            envelope["v"].clone(),
            envelope["ch"].clone(),
            envelope["kind"].clone()
        ),
        (json!(1), json!("/app/orders/0zn5ptc"), json!("live"))
    );
    assert_eq!(envelope["data"], json!({ "status": "shipped" }));
    let id = envelope["id"].as_str().unwrap();
    assert!(
        id.len() == 32
            && id
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b)),
        "id {id} is not 32 lowercase hex characters"
    );
    assert!(envelope["ts"].is_u64());
    let claims = read_claims(published[0].authorization.strip_prefix("Bearer ").unwrap());
    assert_eq!(claims["aud"], gateway.host);
    assert_eq!(claims["sub"], "server");
    assert_eq!(
        claims["ocel"],
        json!({ "op": "publish", "ch": "/app/orders/0zn5ptc", "ns": "app" })
    );
}

#[tokio::test]
async fn publish_refuses_params_or_an_event_it_cannot_send() {
    let gateway = FakeGateway::serve();
    let _env = deliver(&gateway.binding()).await;
    let rt = build(Arc::default());

    let empty = rt
        .publish(
            &Orders {
                order_id: String::new(),
            },
            &OrderEvent {
                status: "x".to_string(),
            },
        )
        .await;
    let large = rt
        .publish(
            &Rooms {
                room_id: "r1".to_string(),
            },
            &ChatMessage {
                text: "x".repeat(240 * 1024),
            },
        )
        .await;
    *gateway.status.lock().unwrap() = 401;
    let refused = rt
        .publish(
            &Orders {
                order_id: "o1".to_string(),
            },
            &OrderEvent {
                status: "x".to_string(),
            },
        )
        .await;

    assert!(
        matches!(empty, Err(ocel::Error::PublishRefused { code, .. }) if code == DenialCode::EmptyValue)
    );
    assert!(
        matches!(large, Err(ocel::Error::PublishRefused { code, .. }) if code == DenialCode::BodyTooLarge)
    );
    assert!(
        matches!(refused, Err(ocel::Error::PublishFailed { said, .. }) if said.contains("status 401"))
    );
}

#[tokio::test]
async fn publish_outside_a_provisioned_run_says_why() {
    let _env = deliver_appsync().await;
    let rt = build(Arc::default());
    let event = OrderEvent {
        status: "x".to_string(),
    };
    let orders = Orders {
        order_id: "o1".to_string(),
    };

    let unsupported = rt.publish(&orders, &event).await;
    std::env::set_var("OCEL_PHASE", "discovery");
    let unprovisioned = rt.publish(&orders, &event).await;
    std::env::remove_var("OCEL_PHASE");
    std::env::remove_var("OCEL_RESOURCE_REALTIME_app");
    let missing = rt.publish(&orders, &event).await;

    assert!(
        matches!(unsupported, Err(ocel::Error::PublishFailed { said, .. }) if said.contains("AppSync Events is not supported yet"))
    );
    assert!(matches!(
        unprovisioned,
        Err(ocel::Error::Unprovisioned { .. })
    ));
    assert!(
        matches!(missing, Err(ocel::Error::MissingBinding { key }) if key == "OCEL_RESOURCE_REALTIME_app")
    );
}

#[test]
fn a_builder_outside_the_contract_is_refused_saying_why() {
    let full = || {
        Realtime::builder("app")
            .authorize(|_| async { Ok::<Option<Caller>, String>(None) })
            .subscribe::<Orders>(|_| async { Ok::<_, String>(true) })
            .subscribe::<Deploys>(|_| async { Ok::<_, String>(true) })
            .subscribe::<Rooms>(|_| async { Ok::<_, String>(true) })
    };
    let cases: Vec<(&str, Result<Realtime, ocel::Error>)> = vec![
        (
            "no publish rule for channel \"rooms/:room_id\"",
            full().build(),
        ),
        (
            "has a subscribe rule for channel \"status\" declared public",
            full()
                .publish::<Rooms>(|_| async { Ok::<_, String>(true) })
                .subscribe::<Status>(|_| async { Ok::<_, String>(true) })
                .build(),
        ),
        (
            "no subscribe rule for channel \"orders/:order_id\"",
            Realtime::builder("app")
                .authorize(|_| async { Ok::<Option<Caller>, String>(None) })
                .build(),
        ),
        (
            "authorize comes before every rule",
            Realtime::builder("app")
                .subscribe::<Orders>(|_| async { Ok::<_, String>(true) })
                .authorize(|_| async { Ok::<Option<Caller>, String>(None) })
                .build(),
        ),
        ("channel namespace", Realtime::builder("my_app").build()),
        (
            "has rules and no authorize",
            Realtime::builder("app")
                .subscribe::<Orders>(|_| async { Ok::<_, String>(true) })
                .subscribe::<Deploys>(|_| async { Ok::<_, String>(true) })
                .subscribe::<Rooms>(|_| async { Ok::<_, String>(true) })
                .publish::<Rooms>(|_| async { Ok::<_, String>(true) })
                .build(),
        ),
    ];
    for (reason, built) in cases {
        let refused = built.err().map(|err| err.to_string()).unwrap_or_default();
        assert!(
            refused.contains(reason),
            "{refused:?} does not say {reason:?}"
        );
    }
}

#[tokio::test]
async fn publish_refuses_a_channel_of_another_realtime_resource() {
    let gateway = FakeGateway::serve();
    let _env = deliver(&gateway.binding()).await;
    let rt = build(Arc::default());

    let refused = rt
        .publish(
            &ChatOrders {
                order_id: "o1".to_string(),
            },
            &OrderEvent {
                status: "x".to_string(),
            },
        )
        .await;

    assert!(
        matches!(refused, Err(ocel::Error::PublishRefused { code, .. }) if code == DenialCode::UnknownPattern)
    );
    assert!(gateway.published.lock().unwrap().is_empty());
}

#[tokio::test]
async fn a_token_the_handler_cannot_mint_answers_500_uncacheable() {
    let _env = deliver(&deliver_binding_with("signingKey", json!("AAAAAAA="))).await;
    let rt = build(Arc::default());

    let subscribe = call(
        &rt,
        "POST",
        &[],
        r#"{"ops":[{"op":"subscribe","pattern":"status"}]}"#,
    )
    .await;
    let connect = call(&rt, "POST", &[], r#"{"connect":true,"ops":[]}"#).await;

    for answered in [subscribe, connect] {
        assert_eq!(answered.status, 500, "{}", answered.body);
        assert_eq!(answered.headers["cache-control"], "no-store");
        assert!(answered.body["error"].is_string());
    }
}

#[tokio::test]
async fn a_rule_that_panics_denies_its_op_with_rule_error() {
    let _env = deliver_appsync().await;
    async fn break_rule(_: SubscribeContext<Caller, Orders>) -> Result<bool, String> {
        panic!("the rule broke")
    }
    let rt = build_ruled(break_rule, |_| async { Ok::<_, String>(true) });

    let res = post(
        &rt,
        json!({ "ops": [
            { "op": "subscribe", "pattern": "orders/:order_id", "params": { "order_id": "o-1" } },
            { "op": "subscribe", "pattern": "status" },
        ] }),
        &[("x-user", "u1")],
    )
    .await;

    assert_eq!(res["denied"], json!([{ "i": 0, "code": "rule-error" }]));
    assert_eq!(res["grants"][0]["i"], 1);
}

#[tokio::test]
async fn an_authorize_that_panics_answers_500_uncacheable() {
    let _env = deliver_appsync().await;
    async fn break_authorize(_: Arc<Request>) -> Result<Option<Caller>, String> {
        panic!("the session store broke")
    }
    let rt = Realtime::builder("app")
        .authorize(break_authorize)
        .subscribe::<Orders>(|_| async { Ok::<_, String>(true) })
        .subscribe::<Deploys>(|_| async { Ok::<_, String>(true) })
        .subscribe::<Rooms>(|_| async { Ok::<_, String>(true) })
        .publish::<Rooms>(|_| async { Ok::<_, String>(true) })
        .build()
        .unwrap();

    let answered = call(&rt, "POST", &[], r#"{"ops":[]}"#).await;

    assert_eq!(answered.status, 500);
    assert_eq!(answered.headers["cache-control"], "no-store");
}

#[tokio::test]
async fn a_binding_the_handler_cannot_use_answers_500_uncacheable() {
    let unknown = deliver_binding_with("transport", json!("REALTIME_TRANSPORT_UNSPECIFIED"));
    let _env = deliver(&unknown).await;
    let rt = build(Arc::default());
    let ops = r#"{"connect":true,"ops":[{"op":"subscribe","pattern":"status"}]}"#;

    let unknown_transport = call(&rt, "POST", &[], ops).await;
    std::env::remove_var("OCEL_RESOURCE_REALTIME_app");
    let missing = call(&rt, "POST", &[], ops).await;

    for answered in [unknown_transport, missing] {
        assert_eq!(answered.status, 500, "{}", answered.body);
        assert_eq!(answered.headers["cache-control"], "no-store");
    }
}

#[tokio::test(start_paused = true)]
async fn a_publish_the_gateway_never_answers_fails_after_10_seconds() {
    let gateway = StalledGateway::serve();
    let _env = deliver(&gateway.binding()).await;
    let rt = build(Arc::default());
    let started = tokio::time::Instant::now();

    let published = tokio::time::timeout(
        Duration::from_secs(60),
        rt.publish(
            &Orders {
                order_id: "o1".to_string(),
            },
            &OrderEvent {
                status: "x".to_string(),
            },
        ),
    )
    .await
    .expect("the publish gave up before 60s");
    let relayed = tokio::time::timeout(
        Duration::from_secs(60),
        post(
            &rt,
            json!({ "ops": [{ "op": "publish", "pattern": "rooms/:room_id", "params": { "room_id": "r1" }, "body": { "text": "hi" } }] }),
            &[("x-user", "u1")],
        ),
    )
    .await
    .expect("the relayed publish gave up before 60s");

    assert!(
        matches!(published, Err(ocel::Error::PublishFailed { said, .. }) if said.contains("within 10s"))
    );
    assert_eq!(
        relayed["denied"],
        json!([{ "i": 0, "code": "publish-failed" }])
    );
    assert_eq!(started.elapsed(), Duration::from_secs(20));
}

#[tokio::test]
async fn relayed_publishes_reach_the_transport_in_batch_order() {
    let gateway = FakeGateway::serve();
    let _env = deliver(&gateway.binding()).await;
    let rt = build_ruled(
        |_| async { Ok::<_, String>(true) },
        |ctx| async move {
            if ctx.body.text == "first" {
                tokio::time::sleep(Duration::from_millis(100)).await;
            }
            Ok::<_, String>(true)
        },
    );
    let publish = |text: &str| json!({ "op": "publish", "pattern": "rooms/:room_id", "params": { "room_id": "r1" }, "body": { "text": text } });

    let res = post(
        &rt,
        json!({ "ops": [publish("first"), publish("second")] }),
        &[("x-user", "u1")],
    )
    .await;

    assert_eq!(res["denied"], json!([]));
    let published = gateway.published.lock().unwrap();
    let texts: Vec<&Value> = published
        .iter()
        .map(|one| &one.envelope["data"]["text"])
        .collect();
    assert_eq!(texts, [&json!("first"), &json!("second")]);
}

#[tokio::test]
async fn the_handler_takes_exactly_the_batch_shape_and_refuses_anything_else() {
    let _env = deliver_appsync().await;
    let rt = build(Arc::default());

    for body in [
        r#"[]"#,
        r#"{"ops":[],"Connect":true}"#,
        r#"{"ops":[],"extra":1}"#,
        r#"{"connect":null,"ops":[]}"#,
        r#"{"connect":"yes","ops":[]}"#,
        r#"{"OPS":[]}"#,
        r#"{"ops":null}"#,
    ] {
        let answered = call(&rt, "POST", &[], body).await;
        assert_eq!(answered.status, 400, "{body}");
        assert_eq!(answered.headers["cache-control"], "no-store");
    }
    assert_eq!(
        call(
            &rt,
            "POST",
            &[("content-type", "Application/JSON; charset=utf-8")],
            r#"{"ops":[]}"#
        )
        .await
        .status,
        200
    );
}

#[tokio::test]
async fn the_handler_denies_each_malformed_op_on_its_own_in_the_order_of_the_checks() {
    let gateway = FakeGateway::serve();
    let _env = deliver(&gateway.binding()).await;
    let rt = build(Arc::default());
    let room = json!({ "room_id": "r1" });

    let res = post(
        &rt,
        json!({ "ops": [
            "subscribe",
            { "op": "subscribe", "pattern": "status", "Params": {} },
            { "op": "subscribe", "pattern": "status", "body": {} },
            { "pattern": "status" },
            { "op": 1, "pattern": "status" },
            { "op": "Subscribe", "pattern": "status" },
            { "op": "subscribe" },
            { "op": "subscribe", "pattern": 7 },
            { "op": "subscribe", "pattern": "status", "params": [] },
            { "op": "subscribe", "pattern": "status", "params": "x" },
            { "op": "subscribe", "pattern": "status", "params": null },
            { "op": "publish", "pattern": "rooms/:room_id", "params": {}, "body": "not a message" },
            { "op": "publish", "pattern": "rooms/:room_id", "params": room },
            { "op": "publish", "pattern": "rooms/:room_id", "params": room, "body": { "text": "hi", "x": 1 } },
            { "op": "publish", "pattern": "rooms/:room_id", "params": room, "body": { "text": "x".repeat(240 * 1024) } },
            { "op": "publish", "pattern": "rooms/:room_id", "params": room, "body": "not a message" },
        ] }),
        &[],
    )
    .await;

    assert_eq!(res["grants"].as_array().unwrap().len(), 1);
    assert_eq!(
        (
            res["grants"][0]["i"].clone(),
            res["grants"][0]["wire"].clone()
        ),
        (json!(10), json!("/app/status"))
    );
    assert_eq!(
        res["denied"],
        json!([
            { "i": 0, "code": "invalid-op" },
            { "i": 1, "code": "invalid-op" },
            { "i": 2, "code": "invalid-op" },
            { "i": 3, "code": "unknown-op" },
            { "i": 4, "code": "unknown-op" },
            { "i": 5, "code": "unknown-op" },
            { "i": 6, "code": "unknown-pattern" },
            { "i": 7, "code": "unknown-pattern" },
            { "i": 8, "code": "invalid-params" },
            { "i": 9, "code": "invalid-params" },
            { "i": 11, "code": "missing-param" },
            { "i": 12, "code": "invalid-body" },
            { "i": 13, "code": "invalid-body" },
            { "i": 14, "code": "body-too-large" },
            { "i": 15, "code": "invalid-body" },
        ])
    );
    assert!(gateway.published.lock().unwrap().is_empty());
}

#[tokio::test]
async fn a_relayed_publish_whose_token_cannot_be_minted_answers_500_and_publishes_nothing() {
    let gateway = FakeGateway::serve();
    let mut binding: Value = serde_json::from_str(&gateway.binding()).unwrap();
    binding["realtime"]["signingKey"] = json!("AAAAAAA=");
    let _env = deliver(&binding.to_string()).await;
    let rt = build(Arc::default());

    let answered = call(
        &rt,
        "POST",
        &[("x-user", "u1"), ("origin", "https://app.example")],
        r#"{"ops":[{"op":"publish","pattern":"rooms/:room_id","params":{"room_id":"r1"},"body":{"text":"hi"}}]}"#,
    )
    .await;

    assert_eq!(answered.status, 500, "{}", answered.body);
    assert_eq!(answered.headers["cache-control"], "no-store");
    assert_eq!(
        answered.headers["access-control-allow-origin"],
        "https://app.example"
    );
    assert!(gateway.published.lock().unwrap().is_empty());
}
