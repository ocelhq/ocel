#![allow(dead_code)]

#[derive(serde::Serialize, serde::Deserialize, schemars::JsonSchema, Clone)]
pub struct Order {
    pub id: u64,
}

#[derive(serde::Serialize, serde::Deserialize, schemars::JsonSchema)]
pub struct Shipped {
    pub at: String,
}

async fn warm() -> Result<(), sdk::RunError> {
    Ok(())
}

#[derive(sdk::Resources)]
#[ocel(crate = sdk)]
pub struct Infra {
    #[ocel(retry(max_attempts = 3), schema)]
    pub orders: sdk::Topic<Order>,
    pub db: sdk::Postgres,
    pub files: sdk::Bucket,
    #[ocel(on_start = warm)]
    pub media: sdk::Worker,
    #[ocel(entries = [Visits])]
    pub cache: sdk::Kv,
}

#[derive(sdk::KvKey)]
#[ocel(crate = sdk, pattern = "visits/:page", counter)]
pub struct Visits {
    pub page: String,
}

#[derive(sdk::Env)]
#[ocel(crate = "::sdk")]
pub struct Env {
    #[ocel(default = 3000)]
    pub port: u16,
    pub flag: Option<bool>,
    #[ocel(group)]
    pub github: Option<GitHub>,
}

#[derive(sdk::Group)]
#[ocel(crate = sdk)]
pub struct GitHub {
    pub client_id: String,
}

#[derive(sdk::Channel)]
#[ocel(crate = sdk, realtime = "app", pattern = "orders/:order_id", event = Shipped, schema)]
pub struct OrderChannel {
    pub order_id: String,
}

async fn notify(_payload: &Order, _output: &u32, _run: &sdk::Run) -> Result<(), sdk::RunError> {
    Ok(())
}

#[sdk::task(crate = sdk, retry(max_attempts = 2), schema, on_success = notify)]
async fn resize(payload: Order, _run: &sdk::Run) -> Result<u32, sdk::RunError> {
    Ok(payload.id as u32)
}

#[sdk::task(crate = sdk, batch(size = 10))]
async fn digest(_payloads: Vec<Order>, _run: &sdk::Run) -> Result<(), sdk::RunError> {
    Ok(())
}

#[sdk::consumer(crate = sdk, topic = Infra::ORDERS, lanes = ["high"])]
async fn ship(_payload: Order, _run: &sdk::Run) -> Result<(), sdk::RunError> {
    Ok(())
}

#[sdk::batch_consumer(crate = sdk, topic = Infra::ORDERS, batch_size = 10)]
async fn audit(_payloads: Vec<Order>, _run: &sdk::Run) -> Result<(), sdk::RunError> {
    Ok(())
}

#[sdk::main(crate = sdk)]
fn start() {}

#[test]
fn every_macro_reaches_the_sdk_through_the_path_its_crate_attribute_names() {
    let resources: Vec<&str> = <Infra as sdk::Declare>::declared()
        .resources
        .iter()
        .map(|resource| resource.name)
        .collect();
    assert_eq!(resources, ["orders", "db", "files", "media", "cache"]);

    let variables: Vec<&str> = <Env as sdk::Declare>::declared()
        .variables
        .iter()
        .map(|variable| variable.key)
        .collect();
    assert_eq!(variables, ["PORT", "FLAG"]);

    assert_eq!(resize.name(), "resize");
    assert_eq!(Infra::ORDERS.name(), "orders");
    assert_eq!(<OrderChannel as sdk::Channel>::PATTERN, "orders/:order_id");
}
