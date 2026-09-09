mod collector;

use collector::{cell, holding, Received, DECLARE, DECLARE_ENV, REPORT_ENV_PROBLEMS};
use ocel::proto::app::resources::v1::declare_request::Config;
use ocel::proto::common::links::v1::LinkType;

#[derive(ocel::Resources)]
struct Infra {
    #[ocel(name = "main")]
    db: ocel::Postgres,
    #[ocel(name = "cache", version = "16")]
    cache: ocel::Postgres,
}

#[allow(dead_code)]
#[derive(ocel::Env)]
struct Env {
    database_name: String,
    #[ocel(default = 3000)]
    port: u16,
    #[ocel(key = "MISSING_ONE", sensitive)]
    missing_one: u16,
    #[ocel(sensitive)]
    api_port: u16,
    #[ocel(key = "SCOPED", folders = ["/apps/web", "/apps/api"])]
    scoped: String,
    ready: bool,
    started: bool,
}

const DB_LINE: &str = "10";

#[test]
fn discovery_posts_every_declaration_and_reports_the_problems_it_finds() {
    let (url, requests) = holding(
        4,
        vec![
            cell("DATABASE_NAME", "", "shop"),
            cell("PORT", "", "eighty"),
            cell("API_PORT", "", "s3cret-and-unparseable"),
            cell("SCOPED", "/apps/web", "eu"),
            cell("READY", "", "yes"),
            cell("STARTED", "", "T"),
        ],
    );
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

    let received: Vec<Received> = (0..4)
        .map(|_| requests.recv().expect("a request"))
        .collect();
    assert!(
        received
            .iter()
            .all(|one| one.protocol.as_deref() == Some("1")),
        "a request went out without the connect protocol version"
    );

    let declares: Vec<_> = received
        .iter()
        .filter(|one| one.path == DECLARE)
        .map(Received::declare)
        .collect();
    assert_eq!(declares.len(), 2);

    let main = declares
        .iter()
        .find(|sent| sent.resource.name == "main")
        .expect("the postgres named main");
    let cache = declares
        .iter()
        .find(|sent| sent.resource.name == "cache")
        .expect("the postgres named cache");
    assert_eq!(main.resource.r#type, LinkType::LINK_TYPE_POSTGRES);
    assert_eq!(version(main), "17");
    assert_eq!(version(cache), "16");

    let (file, line) = main
        .source
        .rsplit_once(':')
        .expect("a file and a line in the source");
    assert_eq!(
        std::path::Path::new(file),
        workspace.join("ocel/tests/declare.rs"),
        "source = {}, want the file the field is written in",
        main.source
    );
    assert_eq!(line, DB_LINE);

    let infra = Infra::load().expect("the struct loads");
    assert_eq!(infra.db.name(), "main");
    assert_eq!(infra.cache.name(), "cache");

    let env = received
        .iter()
        .find(|one| one.path == DECLARE_ENV)
        .expect("one DeclareEnv request")
        .declare_env();
    let shapes: Vec<String> = env
        .definitions
        .iter()
        .map(|definition| {
            format!(
                "{} {:?} required={} schema={}",
                definition.key, definition.class, definition.required, definition.has_schema,
            )
        })
        .collect();
    assert_eq!(
        shapes,
        [
            "DATABASE_NAME VARIABLE_CLASS_PLAIN required=true schema=false",
            "PORT VARIABLE_CLASS_PLAIN required=false schema=true",
            "MISSING_ONE VARIABLE_CLASS_SENSITIVE required=true schema=true",
            "API_PORT VARIABLE_CLASS_SENSITIVE required=true schema=true",
            "SCOPED VARIABLE_CLASS_PLAIN required=true schema=false",
            "READY VARIABLE_CLASS_PLAIN required=true schema=true",
            "STARTED VARIABLE_CLASS_PLAIN required=true schema=true",
        ]
    );

    let reported = received
        .iter()
        .find(|one| one.path == REPORT_ENV_PROBLEMS)
        .expect("one ReportEnvProblems request")
        .problems();
    let problems: Vec<String> = reported
        .problems
        .iter()
        .map(|problem| {
            format!(
                "{} '{}' {:?} {}",
                problem.key, problem.folder, problem.kind, problem.detail
            )
        })
        .collect();
    assert_eq!(
        problems,
        [
            "PORT '' KIND_INVALID invalid digit found in string",
            "MISSING_ONE '' KIND_MISSING ",
            "API_PORT '' KIND_INVALID withheld, because a 'sensitive' value's parse message can quote the value itself",
            "SCOPED '/apps/api' KIND_MISSING ",
            "READY '' KIND_INVALID want one of 1 t T TRUE true True 0 f F FALSE false False",
        ]
    );
    assert!(
        !reported
            .problems
            .iter()
            .any(|problem| problem.detail.contains("s3cret")),
        "a problem quoted a sensitive value back"
    );
}

fn version(declared: &ocel::proto::app::resources::v1::DeclareRequest) -> &str {
    match declared.config.as_ref().expect("a config") {
        Config::Postgres(config) => config.version.as_str(),
        other => panic!("declared {other:?}, want a postgres config"),
    }
}
