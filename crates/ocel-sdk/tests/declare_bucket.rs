mod collector;

use collector::{collector, Received, DECLARE};
use ocel::proto::app::resources::v1::declare_request::Config;
use ocel::proto::app::resources::v1::ResourceType;

#[derive(ocel::Resources)]
struct Infra {
    #[ocel(name = "main")]
    db: ocel::Postgres,
    #[ocel(name = "avatars", public, allowed_origins = ["https://shop.example"])]
    avatars: ocel::Bucket,
    uploads: ocel::Bucket,
}

#[test]
fn a_struct_declares_its_buckets_beside_its_databases() {
    let (url, requests) = collector(3);
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", &url);
    std::env::set_var("OCEL_DEV_SERVER_TOKEN", collector::TOKEN);

    assert!(
        ocel::discover().expect("discover"),
        "discover ran the app on"
    );

    let declares: Vec<_> = (0..3)
        .map(|_| requests.recv().expect("a request"))
        .filter(|one| one.path == DECLARE)
        .map(|one| Received::declare(&one))
        .collect();
    let shapes: Vec<String> = declares
        .iter()
        .map(|sent| {
            let kind = match sent.config.as_ref().expect("a config") {
                Config::Postgres(config) => format!("postgres version={}", config.version),
                Config::Bucket(config) => format!(
                    "bucket public={} origins={:?}",
                    config.public, config.allowed_origins
                ),
            };
            format!("{:?} {} {kind}", sent.resource.r#type, sent.resource.name)
        })
        .collect();
    assert_eq!(
        shapes,
        [
            "RESOURCE_TYPE_POSTGRES main postgres version=17",
            "RESOURCE_TYPE_BUCKET avatars bucket public=true origins=[\"https://shop.example\"]",
            "RESOURCE_TYPE_BUCKET uploads bucket public=false origins=[]",
        ]
    );
    assert_eq!(
        declares[1].resource.r#type,
        ResourceType::RESOURCE_TYPE_BUCKET
    );

    let infra = Infra::load().expect("the struct loads");
    assert_eq!(infra.db.name(), "main");
    assert_eq!(infra.avatars.name(), "avatars");
    assert_eq!(infra.uploads.name(), "uploads");
}
