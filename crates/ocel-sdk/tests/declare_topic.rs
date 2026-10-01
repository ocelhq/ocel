mod collector;

use collector::{collector, Received, DECLARE};
use ocel::proto::app::resources::v1::declare_request::Config;
use ocel::proto::app::resources::v1::ResourceType;

#[derive(serde::Serialize, serde::Deserialize)]
pub struct Order {
    pub id: u64,
}

async fn warm() -> Result<(), ocel::RunError> {
    Ok(())
}

async fn time_run(_run: &ocel::Run, next: ocel::Next<'_>) -> Result<(), ocel::RunError> {
    next.await
}

#[derive(ocel::Resources)]
struct Infra {
    #[ocel(name = "orders", ordered, retry(max_attempts = 7, max_delay = "2m"))]
    orders: ocel::Topic<Order>,
    audits: ocel::Topic<Order>,
    #[ocel(name = "media", concurrency = 4, on_start = warm, middleware = time_run)]
    media: ocel::Worker,
}

#[test]
fn a_struct_declares_its_topics_and_workers_and_loads_their_handles() {
    let (url, requests) = collector(3);
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", &url);
    std::env::set_var("OCEL_DEV_SERVER_TOKEN", collector::TOKEN);
    assert!(ocel::discover().expect("discover"));

    let declared: Vec<_> = (0..3)
        .map(|_| requests.recv().expect("a request"))
        .inspect(|one| assert_eq!(one.path, DECLARE))
        .map(|one| Received::declare(&one))
        .collect();

    let orders = &declared[0];
    assert_eq!(
        (orders.resource.r#type, orders.resource.name.as_str()),
        (ResourceType::RESOURCE_TYPE_TOPIC.into(), "orders")
    );
    let Some(Config::Topic(topic)) = &orders.config else {
        panic!("orders declared {:?}", orders.config);
    };
    assert!(topic.ordered);
    assert_eq!(topic.schema, "");
    let retry = topic.retry.as_option().expect("a retry policy");
    assert_eq!(retry.max_attempts, 7);
    assert_eq!(
        retry.max_delay.as_option().expect("a max delay").seconds,
        120
    );
    assert!(retry.min_delay.as_option().is_none());

    let audits = &declared[1];
    let Some(Config::Topic(topic)) = &audits.config else {
        panic!("audits declared {:?}", audits.config);
    };
    assert_eq!(audits.resource.name, "audits");
    assert!(!topic.ordered);
    assert!(topic.retry.as_option().is_none());

    let media = &declared[2];
    assert_eq!(
        (media.resource.r#type, media.resource.name.as_str()),
        (ResourceType::RESOURCE_TYPE_WORKER.into(), "media")
    );
    let Some(Config::Worker(worker)) = &media.config else {
        panic!("media declared {:?}", media.config);
    };
    assert_eq!(worker.concurrency, 4);

    let infra = Infra::load().expect("the struct loads");
    assert_eq!(infra.orders.name(), "orders");
    assert_eq!(infra.audits.name(), "audits");
    assert_eq!(infra.media.name(), "media");
}
