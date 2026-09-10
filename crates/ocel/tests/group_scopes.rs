mod collector;

use collector::{cell, holding, Received, DECLARE_ENV};

#[allow(dead_code)]
#[derive(ocel::Env)]
struct Env {
    #[ocel(group)]
    shared: Option<Shared>,
}

#[allow(dead_code)]
#[derive(ocel::Group)]
struct Shared {
    #[ocel(key = "SHARED_BOTH", folders = ["/web", "/api"])]
    both: String,
    #[ocel(key = "SHARED_API", folders = ["/api"])]
    api: String,
    #[ocel(key = "SHARED_WIDER")]
    wider: String,
}

#[test]
fn a_group_its_members_share_a_folder_declares_every_member() {
    let (url, requests) = holding(2, vec![cell("SHARED_BOTH", "/api", "b")]);
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", &url);

    assert!(
        ocel::discover().expect("discover"),
        "discover ran the app on"
    );

    let received: Vec<Received> = (0..2)
        .map(|_| requests.recv().expect("a request"))
        .collect();
    let declared = received
        .iter()
        .find(|one| one.path == DECLARE_ENV)
        .expect("one DeclareEnv request")
        .declare_env();
    let members: Vec<(String, String)> = declared
        .definitions
        .iter()
        .map(|definition| (definition.key.clone(), definition.group.clone()))
        .collect();
    assert_eq!(
        members,
        [
            ("SHARED_BOTH".to_string(), "shared".to_string()),
            ("SHARED_API".to_string(), "shared".to_string()),
            ("SHARED_WIDER".to_string(), "shared".to_string()),
        ]
    );
}
