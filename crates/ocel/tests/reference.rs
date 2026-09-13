mod collector;

use collector::{collector, Received, DECLARE, REFERENCE};
use ocel::proto::app::resources::v1::ResourceType;

#[allow(dead_code)]
#[derive(ocel::Resources)]
struct Infra {
    #[ocel(name = "main")]
    db: ocel::Postgres,
}

#[allow(dead_code)]
#[derive(ocel::Resources)]
struct Shared {
    #[ocel(name = "main", reference)]
    db: ocel::Postgres,
}

const REFERENCE_LINE: &str = "17";

#[test]
fn a_reference_posts_beside_the_declaration_it_names_without_claiming_the_name() {
    let (url, requests) = collector(2);
    let workspace = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .expect("the workspace above the crate");
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_SOURCE_ROOT", workspace);
    std::env::set_var("OCEL_DEV_SERVER", &url);

    assert!(
        ocel::discover().expect("a declaration and a reference to it are not a duplicate"),
        "discover ran the app on"
    );

    let received: Vec<Received> = (0..2)
        .map(|_| requests.recv().expect("a request"))
        .collect();
    assert_eq!(
        received.iter().filter(|one| one.path == DECLARE).count(),
        1,
        "the reference was posted as a declaration"
    );
    let reference = received
        .iter()
        .find(|one| one.path == REFERENCE)
        .expect("one Reference request")
        .reference();
    assert_eq!(reference.resource.name, "main");
    assert_eq!(
        reference.resource.r#type,
        ResourceType::RESOURCE_TYPE_POSTGRES
    );
    let (file, line) = reference
        .source
        .rsplit_once(':')
        .expect("a file and a line in the source");
    assert_eq!(
        std::path::Path::new(file),
        workspace.join("ocel/tests/reference.rs"),
        "source = {}, want the file the field is written in",
        reference.source
    );
    assert_eq!(line, REFERENCE_LINE);

    let shared = Shared::load().expect("the struct loads");
    assert_eq!(shared.db.name(), "main");
}
