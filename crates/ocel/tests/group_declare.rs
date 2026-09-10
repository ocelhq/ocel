mod collector;

use collector::{cell, holding, Received, DECLARE_ENV, REPORT_ENV_PROBLEMS};

#[allow(dead_code)]
#[derive(ocel::Env)]
struct Env {
    #[ocel(key = "DATABASE_URL")]
    database_url: String,
    /// Enable GitHub sign-in
    #[ocel(group)]
    github: Option<GitHub>,
    #[ocel(group)]
    smtp: Smtp,
    #[ocel(group)]
    stripe: Option<Stripe>,
}

#[allow(dead_code)]
#[derive(ocel::Env)]
struct GitHub {
    #[ocel(key = "GITHUB_CLIENT_ID")]
    client_id: String,
    #[ocel(key = "GITHUB_CLIENT_SECRET")]
    client_secret: ocel::Secret,
}

#[allow(dead_code)]
#[derive(ocel::Env)]
struct Smtp {
    #[ocel(key = "SMTP_HOST")]
    host: String,
    #[ocel(key = "SMTP_PORT", default = 587)]
    port: u16,
    #[ocel(key = "SMTP_USER")]
    user: Option<String>,
}

#[allow(dead_code)]
#[derive(ocel::Env)]
struct Stripe {
    #[ocel(key = "STRIPE_KEY")]
    key: String,
    #[ocel(key = "STRIPE_WEBHOOK_SECRET")]
    webhook_secret: ocel::Secret,
    #[ocel(key = "STRIPE_MODE")]
    mode: Option<String>,
}

#[test]
fn a_group_reaches_the_dev_server_once_and_owes_only_what_it_is_switched_on_for() {
    let (url, requests) = holding(
        2,
        vec![
            cell("DATABASE_URL", "", "postgres://shop"),
            cell("STRIPE_WEBHOOK_SECRET", "", "whsec_live"),
        ],
    );
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

    let members: Vec<String> = declared
        .definitions
        .iter()
        .map(|definition| {
            format!(
                "{} group='{}' required={}",
                definition.key, definition.group, definition.required
            )
        })
        .collect();
    assert_eq!(
        members,
        [
            "DATABASE_URL group='' required=true",
            "GITHUB_CLIENT_ID group='github' required=true",
            "GITHUB_CLIENT_SECRET group='github' required=true",
            "SMTP_HOST group='smtp' required=true",
            "SMTP_PORT group='smtp' required=false",
            "SMTP_USER group='smtp' required=false",
            "STRIPE_KEY group='stripe' required=true",
            "STRIPE_WEBHOOK_SECRET group='stripe' required=true",
            "STRIPE_MODE group='stripe' required=false",
        ]
    );

    let groups: Vec<String> = declared
        .groups
        .iter()
        .map(|group| {
            format!(
                "{} required={} '{}'",
                group.key, group.required, group.description
            )
        })
        .collect();
    assert_eq!(
        groups,
        [
            "github required=false 'Enable GitHub sign-in'",
            "smtp required=true ''",
            "stripe required=false ''",
        ]
    );

    let problems: Vec<String> = received
        .iter()
        .find(|one| one.path == REPORT_ENV_PROBLEMS)
        .expect("one ReportEnvProblems request")
        .problems()
        .problems
        .iter()
        .map(|problem| format!("{} '{}' {:?}", problem.key, problem.folder, problem.kind))
        .collect();
    assert_eq!(
        problems,
        ["SMTP_HOST '' KIND_MISSING", "STRIPE_KEY '' KIND_MISSING",]
    );
}
