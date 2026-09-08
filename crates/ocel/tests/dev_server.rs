pub static DB: ocel::Postgres = ocel::postgres!("main");

#[test]
fn a_dev_server_that_is_not_a_url_is_reported_before_any_declaration_is_posted() {
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", "not a url");

    let err = ocel::discover().expect_err("a dev server that is not a URL");

    assert_eq!(
        err.to_string(),
        "ocel: OCEL_DEV_SERVER does not hold a URL discovery can post to: 'not a url'"
    );
    assert!(matches!(err, ocel::Error::DevServer { .. }));
    assert_eq!(DB.name(), "main");
}
