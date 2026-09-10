mod collector;

use collector::{cell, holding, Received, REPORT_ENV_PROBLEMS};

#[allow(dead_code)]
#[derive(ocel::Env)]
struct Env {
    #[ocel(group)]
    inherited: Option<Inherited>,
}

#[allow(dead_code)]
#[derive(ocel::Group)]
struct Inherited {
    #[ocel(key = "INHERITED_TOKEN")]
    token: String,
    #[ocel(key = "INHERITED_WEB", folders = ["/web"])]
    web: String,
}

#[test]
fn a_scoped_member_of_a_group_a_root_value_turned_on_is_owed() {
    let (url, requests) = holding(2, vec![cell("INHERITED_TOKEN", "", "t")]);
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", &url);

    assert!(
        ocel::discover().expect("discover"),
        "discover ran the app on"
    );

    let received: Vec<Received> = (0..2)
        .filter_map(|_| {
            requests
                .recv_timeout(std::time::Duration::from_secs(5))
                .ok()
        })
        .collect();
    let problems: Vec<String> = received
        .iter()
        .find(|one| one.path == REPORT_ENV_PROBLEMS)
        .expect("one ReportEnvProblems request")
        .problems()
        .problems
        .iter()
        .map(|problem| format!("{} '{}' {:?}", problem.key, problem.folder, problem.kind))
        .collect();
    assert_eq!(problems, ["INHERITED_WEB '/web' KIND_MISSING"]);
}
