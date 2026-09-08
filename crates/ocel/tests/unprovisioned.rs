static DB: ocel::Postgres = ocel::postgres!("main");

#[test]
fn an_accessor_during_discovery_names_the_declaration_and_the_access() {
    std::env::set_var("OCEL_PHASE", "discovery");
    let err = DB
        .connection_string()
        .expect_err("discovery provisions nothing");
    assert_eq!(
        err.to_string(),
        "'postgres(\"main\")' cannot be used during discovery: tried to access 'connection_string' before the resource was provisioned"
    );
    assert_eq!(DB.name(), "main");
}
