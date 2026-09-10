use ocel::{Error, Postgres};
use std::sync::{Mutex, MutexGuard};
static ENV: Mutex<()> = Mutex::new(());

fn env() -> MutexGuard<'static, ()> {
    ENV.lock().unwrap_or_else(|held| held.into_inner())
}

#[test]
fn a_connection_string_carries_credentials_percent_encoded() {
    let _env = env();
    std::env::set_var(
        "OCEL_RESOURCE_POSTGRES_delivered",
        r#"{"name":"delivered","postgres":{"host":"db.internal","port":5432,"database":"app","username":"user name","password":"p@ss:word/with#odd?chars"}}"#,
    );
    assert_eq!(
        Postgres::new("delivered")
            .connection_string()
            .expect("a connection string"),
        "postgres://user%20name:p%40ss%3Aword%2Fwith%23odd%3Fchars@db.internal:5432/app"
    );
}

#[test]
fn a_binding_that_was_never_delivered_names_the_commands_that_deliver_it() {
    let _env = env();
    let err = Postgres::new("absent")
        .connection_string()
        .expect_err("no binding was delivered");
    assert_eq!(
        err.to_string(),
        "Value for OCEL_RESOURCE_POSTGRES_absent is not defined. Run `ocel dev` to resolve it locally, or `ocel deploy` to have it delivered from the resource this app binds."
    );
}

#[test]
fn a_binding_of_another_kind_says_what_it_carries() {
    let _env = env();
    std::env::set_var(
        "OCEL_RESOURCE_POSTGRES_mistyped",
        r#"{"name":"mistyped","bucket":{"bucket":"uploads"}}"#,
    );
    let err = Postgres::new("mistyped")
        .connection_string()
        .expect_err("a bucket binding");
    assert_eq!(
        err.to_string(),
        "OCEL_RESOURCE_POSTGRES_mistyped carries a BUCKET binding, and this app reads it as a POSTGRES"
    );
    assert!(matches!(err, Error::WrongBindingType { .. }));
}

#[test]
fn a_binding_carrying_nothing_at_all_says_what_it_carries() {
    let _env = env();
    std::env::set_var("OCEL_RESOURCE_POSTGRES_empty", r#"{"name":"empty"}"#);
    let err = Postgres::new("empty")
        .connection_string()
        .expect_err("no properties");
    assert_eq!(
        err.to_string(),
        "OCEL_RESOURCE_POSTGRES_empty carries a UNSPECIFIED binding, and this app reads it as a POSTGRES"
    );
}

#[test]
fn a_binding_the_deploy_delivers_is_read_past_the_fields_this_app_uses() {
    let _env = env();
    let fixture = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../proto/common/bindings/v1/fixtures/postgres.json");
    std::env::set_var(
        "OCEL_RESOURCE_POSTGRES_fixture",
        std::fs::read_to_string(fixture).expect("the postgres binding fixture"),
    );
    assert_eq!(
        Postgres::new("fixture").connection_string().expect("a connection string"),
        "postgres://fixture_operator:fixture-password-not-a-secret@shop-prod-main-r1a2b3c4.cluster-cxyz.us-east-1.rds.amazonaws.com:5433/fixture_catalog"
    );
}

#[test]
fn a_value_that_is_not_a_binding_record_is_reported_without_quoting_what_it_held() {
    let _env = env();
    std::env::set_var("OCEL_RESOURCE_POSTGRES_unreadable", "s3cret-not-json");
    let err = Postgres::new("unreadable")
        .connection_string()
        .expect_err("not a binding record");
    assert!(
        !err.to_string().contains("s3cret"),
        "error = {err}, want it to name the key without the value it held"
    );
    assert_eq!(
        err.to_string(),
        "OCEL_RESOURCE_POSTGRES_unreadable does not carry a binding record, so this app cannot read it as a POSTGRES"
    );
}

#[test]
fn a_run_that_is_not_discovery_leaves_the_app_to_serve() {
    let _env = env();
    assert!(!ocel::discover().expect("discover"));
}

#[cfg(feature = "postgres")]
#[test]
#[ignore = "needs a postgres at DATABASE_URL"]
fn a_pool_is_opened_once_over_the_delivered_binding() {
    let _env = env();
    let runtime = tokio::runtime::Runtime::new().expect("a runtime");
    let url = std::env::var("DATABASE_URL").expect("DATABASE_URL");
    std::env::set_var(
        "OCEL_RESOURCE_POSTGRES_pooled",
        serde_json::json!({"name": "pooled", "postgres": properties(&url)}).to_string(),
    );
    let pooled = Postgres::new("pooled");
    let first = runtime.block_on(pooled.pool()).expect("a pool");
    let second = runtime.block_on(pooled.pool()).expect("the same pool");
    assert!(std::ptr::eq(first, second), "pool opened twice");
}

#[cfg(feature = "postgres")]
fn properties(url: &str) -> serde_json::Value {
    let (credentials, rest) = url
        .trim_start_matches("postgres://")
        .split_once('@')
        .expect("a host in DATABASE_URL");
    let (username, password) = credentials.split_once(':').unwrap_or((credentials, ""));
    let (authority, database) = rest.split_once('/').expect("a database in DATABASE_URL");
    let (host, port) = authority.split_once(':').unwrap_or((authority, "5432"));
    serde_json::json!({
        "host": host,
        "port": port.parse::<u16>().expect("a port"),
        "database": database,
        "username": username,
        "password": password,
    })
}
