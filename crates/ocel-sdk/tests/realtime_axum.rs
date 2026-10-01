#![cfg(all(feature = "axum", feature = "realtime"))]

mod realtime;

use ocel::realtime::Realtime;
use realtime::{read_claims, read_fixture};
use tower::ServiceExt;

#[allow(dead_code)]
#[derive(serde::Serialize, serde::Deserialize)]
struct Status {
    up: bool,
}

#[allow(dead_code)]
#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "status", event = Status, public)]
struct StatusChannel;

#[tokio::test]
async fn the_axum_router_serves_the_handler_on_the_path_it_is_mounted_at() {
    std::env::remove_var("OCEL_PHASE");
    std::env::set_var("OCEL_RESOURCE_REALTIME_app", read_fixture().to_string());
    let rt = Realtime::builder("app")
        .build()
        .expect("the realtime resource builds");
    let app = axum::Router::new().nest_service("/api/realtime", ocel::realtime::axum::router(rt));

    let response = app
        .oneshot(
            http::Request::post("/api/realtime")
                .header("host", "shop.example")
                .header("content-type", "application/json")
                .body(axum::body::Body::from(
                    r#"{"ops":[{"op":"subscribe","pattern":"status","params":{}}]}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), 200);
    assert_eq!(response.headers()["cache-control"], "no-store");
    let body = axum::body::to_bytes(response.into_body(), 1 << 20)
        .await
        .unwrap();
    let answer: serde_json::Value = serde_json::from_slice(&body).unwrap();
    assert_eq!(answer["grants"][0]["wire"], "/app/status");
    assert_eq!(
        read_claims(answer["grants"][0]["token"].as_str().unwrap())["ocel"]["ch"],
        "/app/status"
    );
}

#[tokio::test]
async fn the_axum_router_refuses_a_body_over_1_mib_as_the_handler_refuses_any_request() {
    std::env::remove_var("OCEL_PHASE");
    std::env::set_var("OCEL_RESOURCE_REALTIME_app", read_fixture().to_string());
    let rt = Realtime::builder("app")
        .build()
        .expect("the realtime resource builds");
    let app = axum::Router::new().nest_service("/api/realtime", ocel::realtime::axum::router(rt));

    let response = app
        .oneshot(
            http::Request::post("/api/realtime")
                .header("host", "shop.example")
                .header("content-type", "application/json")
                .body(axum::body::Body::from(vec![b' '; (1 << 20) + 1]))
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), 413);
    assert_eq!(response.headers()["cache-control"], "no-store");
    assert_eq!(response.headers()["content-type"], "application/json");
    let body = axum::body::to_bytes(response.into_body(), 1 << 20)
        .await
        .unwrap();
    let answer: serde_json::Value = serde_json::from_slice(&body).unwrap();
    assert!(answer["error"].is_string());
}
