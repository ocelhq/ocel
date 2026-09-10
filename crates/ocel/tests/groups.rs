use std::sync::{Mutex, MutexGuard};

static ENV: Mutex<()> = Mutex::new(());

fn env() -> MutexGuard<'static, ()> {
    ENV.lock().unwrap_or_else(|held| held.into_inner())
}

fn clear(keys: &[&str]) {
    for key in keys {
        std::env::remove_var(key);
        std::env::remove_var(format!("OCEL_VAR_{key}"));
    }
}

const KEYS: &[&str] = &[
    "DATABASE_URL",
    "GITHUB_CLIENT_ID",
    "GITHUB_CLIENT_SECRET",
    "SMTP_HOST",
    "SMTP_PORT",
    "ANALYTICS_KEY",
    "ANALYTICS_HOST",
    "TELEMETRY_TOKEN",
    "TELEMETRY_SAMPLE",
];

fn standing() {
    clear(KEYS);
    std::env::set_var("DATABASE_URL", "postgres://shop");
    std::env::set_var("SMTP_HOST", "mail.example");
}

#[derive(ocel::Env, Debug)]
struct GitHub {
    #[ocel(key = "GITHUB_CLIENT_ID")]
    client_id: String,
    #[ocel(key = "GITHUB_CLIENT_SECRET")]
    client_secret: ocel::Secret,
}

#[derive(ocel::Env, Debug)]
struct Smtp {
    #[ocel(key = "SMTP_HOST")]
    host: String,
    #[ocel(key = "SMTP_PORT", default = 587)]
    port: u16,
}

#[derive(ocel::Env, Debug)]
struct Analytics {
    #[ocel(key = "ANALYTICS_KEY")]
    key: Option<String>,
    #[ocel(key = "ANALYTICS_HOST", default = "localhost")]
    host: String,
}

#[derive(ocel::Env, Debug)]
struct Telemetry {
    #[ocel(key = "TELEMETRY_TOKEN")]
    token: ocel::Secret,
    #[ocel(key = "TELEMETRY_SAMPLE", default = 10)]
    sample: u16,
}

#[derive(ocel::Env, Debug)]
struct Env {
    #[ocel(key = "DATABASE_URL")]
    database_url: String,
    /// Enable GitHub sign-in
    #[ocel(group)]
    github: Option<GitHub>,
    #[ocel(group)]
    smtp: Smtp,
    #[ocel(group)]
    analytics: Option<Analytics>,
    #[ocel(group)]
    telemetry: Option<Telemetry>,
}

#[test]
fn an_optional_group_is_none_until_a_member_is_delivered() {
    let _env = env();
    standing();

    let loaded = Env::load().expect("the struct loads without the optional group");

    assert!(loaded.github.is_none());
    assert_eq!(loaded.database_url, "postgres://shop");
}

#[test]
fn an_optional_group_missing_one_member_names_the_one_it_is_missing() {
    let _env = env();
    standing();
    std::env::set_var("GITHUB_CLIENT_ID", "id");

    let err = Env::load().expect_err("the group is on and half delivered");

    assert_eq!(
        err.to_string(),
        "'GITHUB_CLIENT_SECRET' has no value. Set one with `ocel env set GITHUB_CLIENT_SECRET=<VALUE>`."
    );
}

#[test]
fn an_optional_group_fully_delivered_loads_with_its_fields_populated() {
    let _env = env();
    standing();
    std::env::set_var("GITHUB_CLIENT_ID", "id");
    std::env::set_var("GITHUB_CLIENT_SECRET", "shhh");

    let loaded = Env::load().expect("the struct loads with the group on");
    let github = loaded.github.expect("the group is on");

    assert_eq!(github.client_id, "id");
    assert_eq!(github.client_secret.value().expect("a value"), "shhh");
}

#[test]
fn a_required_group_reads_its_members_and_its_defaults() {
    let _env = env();
    standing();

    let loaded = Env::load().expect("the struct loads");

    assert_eq!(loaded.smtp.host, "mail.example");
    assert_eq!(loaded.smtp.port, 587);
}

#[test]
fn an_optional_group_whose_members_are_all_optional_stays_off_until_one_is_delivered() {
    let _env = env();
    standing();

    let loaded = Env::load().expect("the struct loads");
    assert!(
        loaded.analytics.is_none(),
        "a group owed nothing switched itself on"
    );

    std::env::set_var("ANALYTICS_KEY", "an-id");
    let loaded = Env::load().expect("the struct loads");
    let analytics = loaded.analytics.expect("the group is on");

    assert_eq!(analytics.key.as_deref(), Some("an-id"));
    assert_eq!(analytics.host, "localhost");
}

#[test]
fn a_live_member_delivered_on_its_own_switches_its_optional_group_on() {
    let _env = env();
    standing();
    std::env::set_var("OCEL_VAR_TELEMETRY_TOKEN", "tk_live");

    let loaded = Env::load().expect("the struct loads");
    let telemetry = loaded
        .telemetry
        .expect("a live member is a delivered member");

    assert_eq!(telemetry.token.value().expect("a value"), "tk_live");
    assert_eq!(telemetry.sample, 10);
}

#[test]
fn a_live_member_switches_its_group_on_even_where_the_group_is_incomplete() {
    let _env = env();
    standing();
    std::env::set_var("OCEL_VAR_GITHUB_CLIENT_SECRET", "shhh");

    let err = Env::load().expect_err("the group is on and owes its client id");

    assert_eq!(
        err.to_string(),
        "'GITHUB_CLIENT_ID' has no value. Set one with `ocel env set GITHUB_CLIENT_ID=<VALUE>`."
    );
}

#[test]
fn a_required_group_with_nothing_delivered_fails_the_load() {
    let _env = env();
    standing();
    clear(&["SMTP_HOST"]);

    let err = Env::load().expect_err("the required group owes its host");

    assert!(matches!(err, ocel::Error::Unset { .. }));
    assert!(
        err.to_string().contains("SMTP_HOST"),
        "error = {err}, want the missing member named"
    );
}
