mod collector;

use collector::{collector, Declared};
use ocel::r#gen::common::links::v1::LinkType;

pub static DB: ocel::Postgres = ocel::postgres!("main");
pub static CACHE: ocel::Postgres = ocel::postgres!("cache", version = "16");

#[test]
fn discovery_posts_a_declaration_carrying_an_absolute_source() {
    let (url, requests) = collector(2);
    let workspace = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .expect("the workspace above the crate");
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_SOURCE_ROOT", workspace);
    std::env::set_var("OCEL_DEV_SERVER", &url);

    assert!(
        ocel::discover().expect("discover"),
        "discover ran the app on"
    );

    let mut declared = Vec::new();
    for _ in 0..2 {
        let one = requests.recv().expect("a declaration");
        assert_eq!(one.path, "/app.resources.v1.ResourceService/Declare");
        assert_eq!(one.protocol.as_deref(), Some("1"));
        declared.push(one);
    }
    let sent = declaration(&declared, "main");
    let cache = declaration(&declared, "cache");
    assert_eq!(version(cache), "16");
    assert_eq!(CACHE.name(), "cache");
    assert_eq!(sent.request.resource.r#type, LinkType::LINK_TYPE_POSTGRES);
    assert_eq!(version(sent), "17");

    let source = sent.request.source.as_str();
    let (file, line) = source.rsplit_once(':').expect("a file and a line");
    assert!(
        std::path::Path::new(file).is_absolute(),
        "source = {source}, want an absolute path"
    );
    assert_eq!(
        std::path::Path::new(file),
        workspace.join("ocel/tests/declare.rs"),
        "source = {source}, want the file the declaration is written in"
    );
    assert_eq!(line, "6");
    assert_eq!(DB.name(), "main");
}

fn declaration<'a>(declared: &'a [Declared], name: &str) -> &'a Declared {
    declared
        .iter()
        .find(|sent| sent.request.resource.name == name)
        .unwrap_or_else(|| panic!("no declaration named {name}"))
}

fn version(declared: &Declared) -> &str {
    use ocel::r#gen::app::resources::v1::declare_request::Config;
    match declared.request.config.as_ref().expect("a config") {
        Config::Postgres(config) => config.version.as_str(),
        other => panic!("declared {other:?}, want a postgres config"),
    }
}
