#[ocel::main]
fn run() {
    panic!("the app ran under discovery");
}

#[ocel::main]
fn fallible() -> Result<(), ocel::Error> {
    panic!("the app ran under discovery");
}

#[test]
fn discovery_returns_from_main_before_the_app_body() {
    std::env::set_var("OCEL_PHASE", "discovery");
    run();
    fallible().expect("discovery");
}
