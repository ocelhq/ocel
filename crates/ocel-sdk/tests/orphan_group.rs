mod collector;

use collector::{cell, collector_with_cells, Received, DECLARE_ENV};

#[allow(dead_code)]
#[derive(ocel::Env)]
struct Env {
    #[ocel(key = "DATABASE_URL")]
    database_url: String,
}

#[allow(dead_code)]
#[derive(ocel::Group)]
struct GitHub {
    #[ocel(key = "GITHUB_CLIENT_ID")]
    client_id: String,
    #[ocel(key = "GITHUB_CLIENT_SECRET")]
    client_secret: String,
}

#[test]
fn a_group_nothing_references_declares_nothing() {
    let (url, requests) =
        collector_with_cells(1, vec![cell("DATABASE_URL", "", "postgres://shop")]);
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", &url);
    std::env::set_var("OCEL_DEV_SERVER_TOKEN", collector::TOKEN);

    assert!(
        ocel::discover().expect("discover"),
        "discover ran the app on"
    );

    let received: Received = requests.recv().expect("a request");
    assert_eq!(received.path, DECLARE_ENV);
    let declared = received.declare_env();

    let keys: Vec<&str> = declared
        .definitions
        .iter()
        .map(|definition| definition.key.as_str())
        .collect();
    assert_eq!(keys, ["DATABASE_URL"]);
    assert!(
        declared.groups.is_empty(),
        "groups = {:?}, want nothing from a group no struct field names",
        declared.groups
    );
}
