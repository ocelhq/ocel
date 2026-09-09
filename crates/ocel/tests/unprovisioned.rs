#[test]
fn an_accessor_during_discovery_names_the_declaration_and_the_access() {
    std::env::set_var("OCEL_PHASE", "discovery");
    let db = ocel::Postgres::new("main");
    let err = db
        .connection_string()
        .expect_err("discovery provisions nothing");
    assert_eq!(
        err.to_string(),
        "'postgres(\"main\")' cannot be used during discovery: tried to access 'connection_string' before the resource was provisioned"
    );
    assert_eq!(db.name(), "main");
}
