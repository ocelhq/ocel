#![cfg(feature = "schemars")]

mod collector;

use collector::{collector, Received};
use ocel::proto::app::resources::v1::declare_request::Config;

#[derive(serde::Serialize, serde::Deserialize, schemars::JsonSchema)]
pub struct Image {
    pub url: String,
    pub width: u32,
}

#[derive(serde::Serialize, serde::Deserialize, schemars::JsonSchema)]
pub struct Order {
    pub id: u64,
}

#[ocel::task(schema, batch(size = 5))]
async fn resize(_images: Vec<Image>, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

#[derive(ocel::Resources)]
#[allow(dead_code)]
struct Infra {
    #[ocel(schema)]
    orders: ocel::Topic<Order>,
}

#[test]
fn schema_declares_the_json_schema_of_one_payload() {
    let (url, requests) = collector(2);
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", &url);
    std::env::set_var("OCEL_DEV_SERVER_TOKEN", collector::TOKEN);
    assert!(ocel::discover().expect("discover"));

    let schemas: Vec<serde_json::Value> = (0..2)
        .map(|_| Received::declare(&requests.recv().expect("a request")))
        .map(|declared| match declared.config {
            Some(Config::Task(task)) => task.schema,
            Some(Config::Topic(topic)) => topic.schema,
            other => panic!("declared {other:?}"),
        })
        .map(|schema| serde_json::from_str(&schema).expect("a JSON Schema"))
        .collect();
    let image = schemas
        .iter()
        .find(|schema| schema["title"] == "Image")
        .expect("the task's schema");
    assert_eq!(image["type"], "object");
    assert_eq!(image["properties"]["url"]["type"], "string");
    assert_eq!(image["required"], serde_json::json!(["url", "width"]));
    assert!(image["$schema"]
        .as_str()
        .expect("a dialect")
        .contains("2020-12"));
    let order = schemas
        .iter()
        .find(|schema| schema["title"] == "Order")
        .expect("the topic's schema");
    assert_eq!(order["properties"]["id"]["type"], "integer");
}
