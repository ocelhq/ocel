#![cfg(all(feature = "schemars", feature = "realtime"))]

mod collector;

use collector::{collector, Received, DECLARE};
use ocel::proto::app::resources::v1::declare_request::Config;
use ocel::proto::app::resources::v1::{RealtimePublish, RealtimeSubscribe, ResourceType};

#[derive(serde::Serialize, serde::Deserialize, schemars::JsonSchema)]
struct OrderEvent {
    status: String,
}

#[allow(dead_code)]
#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "orders/:order_id", event = OrderEvent, schema)]
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

#[allow(dead_code)]
#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "rooms/:room_id", event = OrderEvent, publish, token_ttl = "45s")]
struct Rooms {
    room_id: String,
}

#[allow(dead_code)]
#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "status", event = OrderEvent, public)]
struct Status;

#[allow(dead_code)]
#[derive(ocel::Channel)]
#[ocel(realtime = "chat", pattern = "lobby", event = OrderEvent, public)]
struct Lobby;

#[test]
fn every_channel_declares_its_realtime_resource_with_its_pattern_wildcard_schema_access_and_token_ttl(
) {
    let (url, requests) = collector(2);
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", &url);
    std::env::set_var("OCEL_DEV_SERVER_TOKEN", collector::TOKEN);

    assert!(
        ocel::discover().expect("discover"),
        "discover ran the app on"
    );

    let declares: Vec<_> = (0..2)
        .map(|_| requests.recv().expect("a request"))
        .filter(|one| one.path == DECLARE)
        .map(|one| Received::declare(&one))
        .collect();
    let app = declares
        .iter()
        .find(|one| one.resource.name == "app")
        .expect("realtime app declared");
    assert_eq!(app.resource.r#type, ResourceType::RESOURCE_TYPE_REALTIME);
    assert!(
        app.source.ends_with("declare_realtime.rs:17"),
        "source = {}",
        app.source
    );
    let Some(Config::Realtime(config)) = app.config.as_ref() else {
        panic!("app declared {:?}, want a realtime config", app.config);
    };
    let channels: Vec<_> = config
        .channels
        .iter()
        .map(|channel| {
            (
                channel.pattern.as_str(),
                channel.wildcard,
                channel.subscribe,
                channel.publish,
                channel
                    .source
                    .rsplit(':')
                    .next()
                    .unwrap_or_default()
                    .to_string(),
            )
        })
        .collect();
    assert_eq!(
        channels,
        [
            (
                "orders/:order_id",
                false,
                RealtimeSubscribe::REALTIME_SUBSCRIBE_RULE.into(),
                RealtimePublish::REALTIME_PUBLISH_SERVER.into(),
                "17".to_string()
            ),
            (
                "projects/:project_id/deploys/:deploy_id",
                true,
                RealtimeSubscribe::REALTIME_SUBSCRIBE_RULE.into(),
                RealtimePublish::REALTIME_PUBLISH_SERVER.into(),
                "24".to_string()
            ),
            (
                "rooms/:room_id",
                false,
                RealtimeSubscribe::REALTIME_SUBSCRIBE_RULE.into(),
                RealtimePublish::REALTIME_PUBLISH_RULE.into(),
                "32".to_string()
            ),
            (
                "status",
                false,
                RealtimeSubscribe::REALTIME_SUBSCRIBE_PUBLIC.into(),
                RealtimePublish::REALTIME_PUBLISH_SERVER.into(),
                "39".to_string()
            ),
        ]
    );
    let schema: serde_json::Value =
        serde_json::from_str(&config.channels[0].schema).expect("a JSON Schema");
    assert_eq!(schema["required"], serde_json::json!(["status"]));
    assert_eq!(config.channels[1].schema, "");
    assert_eq!(
        config
            .token_ttl
            .as_option()
            .map(|ttl| (ttl.seconds, ttl.nanos)),
        Some((45, 0))
    );
    let chat = declares
        .iter()
        .find(|one| one.resource.name == "chat")
        .expect("realtime chat declared");
    let Some(Config::Realtime(chat)) = chat.config.as_ref() else {
        panic!("chat declared {:?}, want a realtime config", chat.config);
    };
    assert_eq!(
        chat.token_ttl
            .as_option()
            .map(|ttl| (ttl.seconds, ttl.nanos)),
        Some((60, 0))
    );
}
