mod collector;

use collector::{collector, Received, DECLARE};
use ocel::proto::app::resources::v1::declare_request::Config;
use ocel::proto::app::resources::v1::{DeclareRequest, ResourceType};
use ocel::proto::app::topic::v1::Lane;

#[derive(Clone, serde::Serialize, serde::Deserialize)]
pub struct Image {
    pub url: String,
}

async fn notify(_payload: &Image, _output: &u32, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

#[ocel::task(
    name = "resize-image",
    retry(max_attempts = 5, min_delay = "1s", max_delay = "1m"),
    concurrency = 10,
    max_duration = "5m",
    ttl = "1h",
    ordered,
    worker = "media",
    cron = "0 * * * *",
    on_success = notify
)]
async fn resize(payload: Image, _run: &ocel::Run) -> Result<u32, ocel::RunError> {
    Ok(payload.url.len() as u32)
}

#[ocel::task(batch(size = 10, timeout = "500ms"))]
async fn send_digest(payloads: Vec<Image>, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    let _ = payloads;
    Ok(())
}

#[derive(ocel::Resources)]
#[allow(dead_code)]
struct Infra {
    orders: ocel::Topic<Image>,
}

#[ocel::batch_consumer(
    topic = Infra::ORDERS,
    name = "email",
    retry(max_attempts = 3),
    concurrency = 4,
    lanes = ["high", "default"],
    max_duration = "30s",
    worker = "media",
    batch_size = 5,
    batch_timeout = "2s"
)]
async fn email(payloads: Vec<Image>, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    let _ = payloads;
    Ok(())
}

#[ocel::consumer(topic = Infra::ORDERS)]
async fn audit_log(_payload: Image, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn format_seconds(duration: Option<&buffa_types::google::protobuf::Duration>) -> String {
    let duration = duration.expect("a duration");
    format!("{}s{}n", duration.seconds, duration.nanos)
}

fn discover_declarations() -> Vec<DeclareRequest> {
    let (url, requests) = collector(5);
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", &url);
    std::env::set_var("OCEL_DEV_SERVER_TOKEN", collector::TOKEN);
    assert!(ocel::discover().expect("discover"));
    (0..5)
        .map(|_| requests.recv().expect("a request"))
        .inspect(|one| assert_eq!(one.path, DECLARE))
        .map(|one| Received::declare(&one))
        .collect()
}

#[test]
fn tasks_and_consumers_declare_every_option_they_set_and_leave_the_rest_unset() {
    let declared = discover_declarations();
    let named = |kind: ResourceType, name: &str| {
        declared
            .iter()
            .find(|one| one.resource.r#type == kind && one.resource.name == name)
            .unwrap_or_else(|| panic!("no {kind:?} named {name} was declared"))
    };

    let resized = named(ResourceType::RESOURCE_TYPE_TASK, "resize-image");
    assert!(
        resized.source.ends_with("tests/declare_task.rs:28"),
        "source = {}",
        resized.source
    );
    let Some(Config::Task(task)) = &resized.config else {
        panic!("resize-image declared {:?}", resized.config);
    };
    let retry = task.retry.as_option().expect("a retry policy");
    assert_eq!(retry.max_attempts, 5);
    assert_eq!(format_seconds(retry.min_delay.as_option()), "1s0n");
    assert_eq!(format_seconds(retry.max_delay.as_option()), "60s0n");
    assert_eq!(task.concurrency, 10);
    assert_eq!(format_seconds(task.max_duration.as_option()), "300s0n");
    assert_eq!(format_seconds(task.ttl.as_option()), "3600s0n");
    assert!(task.ordered);
    assert_eq!(task.worker, "media");
    assert_eq!(task.cron, "0 * * * *");
    assert_eq!(task.schema, "");
    assert!(task.batch.as_option().is_none());

    let digest = named(ResourceType::RESOURCE_TYPE_TASK, "send-digest");
    let Some(Config::Task(task)) = &digest.config else {
        panic!("send-digest declared {:?}", digest.config);
    };
    let batch = task.batch.as_option().expect("a batch policy");
    assert_eq!(batch.size, 10);
    assert_eq!(format_seconds(batch.timeout.as_option()), "0s500000000n");
    assert!(task.retry.as_option().is_none());
    assert!(task.max_duration.as_option().is_none());
    assert_eq!(
        (task.concurrency, task.ordered, task.worker.as_str()),
        (0, false, "")
    );

    let email = named(ResourceType::RESOURCE_TYPE_CONSUMER, "email");
    let Some(Config::Consumer(consumer)) = &email.config else {
        panic!("email declared {:?}", email.config);
    };
    assert_eq!(consumer.topic, "orders");
    assert_eq!(consumer.worker, "media");
    assert_eq!(consumer.concurrency, 4);
    assert_eq!(format_seconds(consumer.max_duration.as_option()), "30s0n");
    assert_eq!(
        consumer
            .lanes
            .iter()
            .map(|lane| lane.as_known())
            .collect::<Vec<_>>(),
        [Some(Lane::LANE_HIGH), Some(Lane::LANE_DEFAULT)]
    );
    let retry = consumer.retry.as_option().expect("a retry policy");
    assert_eq!(retry.max_attempts, 3);
    assert!(retry.min_delay.as_option().is_none());
    let batch = consumer.batch.as_option().expect("a batch");
    assert_eq!(batch.size, 5);
    assert_eq!(format_seconds(batch.timeout.as_option()), "2s0n");

    let audit = named(ResourceType::RESOURCE_TYPE_CONSUMER, "audit-log");
    let Some(Config::Consumer(consumer)) = &audit.config else {
        panic!("audit-log declared {:?}", audit.config);
    };
    assert_eq!(
        (consumer.topic.as_str(), consumer.worker.as_str()),
        ("orders", "")
    );
    assert!(consumer.lanes.is_empty());
}

#[test]
fn a_task_handle_carries_the_declared_name() {
    assert_eq!(resize.name(), "resize-image");
    assert_eq!(send_digest.name(), "send-digest");
}
