use ocel::{Error, Postgres};

static DELIVERED: Postgres = Postgres::new("delivered");
static ABSENT: Postgres = Postgres::new("absent");
static MISTYPED: Postgres = Postgres::new("mistyped");
static UNREADABLE: Postgres = Postgres::new("unreadable");

#[test]
fn a_connection_string_carries_credentials_percent_encoded() {
    std::env::set_var(
        "OCEL_RESOURCE_POSTGRES_delivered",
        r#"{"name":"delivered","postgres":{"host":"db.internal","port":5432,"database":"app","username":"user name","password":"p@ss:word/with#odd?chars"}}"#,
    );
    assert_eq!(
        DELIVERED.connection_string().expect("a connection string"),
        "postgres://user%20name:p%40ss%3Aword%2Fwith%23odd%3Fchars@db.internal:5432/app"
    );
}

#[test]
fn a_link_that_was_never_delivered_names_the_commands_that_deliver_it() {
    let err = ABSENT
        .connection_string()
        .expect_err("no link was delivered");
    assert_eq!(
        err.to_string(),
        "Value for OCEL_RESOURCE_POSTGRES_absent is not defined. Run `ocel dev` to resolve it locally, or `ocel deploy` to have it delivered from the resource this app links."
    );
}

#[test]
fn a_link_of_another_kind_says_what_it_carries() {
    std::env::set_var(
        "OCEL_RESOURCE_POSTGRES_mistyped",
        r#"{"name":"mistyped","bucket":{"name":"uploads"}}"#,
    );
    let err = MISTYPED.connection_string().expect_err("a bucket link");
    assert_eq!(
        err.to_string(),
        "OCEL_RESOURCE_POSTGRES_mistyped carries a BUCKET link, and this app reads it as a POSTGRES"
    );
    assert!(matches!(err, Error::WrongLinkType { .. }));
}

#[test]
fn a_value_that_is_not_a_link_record_is_reported_without_quoting_what_it_held() {
    std::env::set_var("OCEL_RESOURCE_POSTGRES_unreadable", "s3cret-not-json");
    let err = UNREADABLE
        .connection_string()
        .expect_err("not a link record");
    assert!(
        !err.to_string().contains("s3cret"),
        "error = {err}, want it to name the key without the value it held"
    );
    assert_eq!(
        err.to_string(),
        "OCEL_RESOURCE_POSTGRES_unreadable does not carry a link record, so this app cannot read it as a POSTGRES"
    );
}

#[test]
fn a_run_that_is_not_discovery_leaves_the_app_to_serve() {
    assert!(!ocel::discover().expect("discover"));
}

#[cfg(feature = "postgres")]
#[test]
#[ignore = "needs a postgres at DATABASE_URL"]
fn a_pool_is_opened_once_over_the_delivered_link() {
    let runtime = tokio::runtime::Runtime::new().expect("a runtime");
    let url = std::env::var("DATABASE_URL").expect("DATABASE_URL");
    std::env::set_var(
        "OCEL_RESOURCE_POSTGRES_pooled",
        serde_json::json!({"name": "pooled", "postgres": properties(&url)}).to_string(),
    );
    static POOLED: Postgres = Postgres::new("pooled");
    let first = runtime.block_on(POOLED.pool()).expect("a pool");
    let second = runtime.block_on(POOLED.pool()).expect("the same pool");
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
