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

#[derive(ocel::Env, Clone, Debug)]
struct Basics {
    database_name: String,
    #[ocel(default = 3000)]
    port: u16,
    timeout: Option<u64>,
}

#[derive(ocel::Env, Debug)]
struct Confidential {
    #[ocel(sensitive)]
    api_key: u16,
    signing_key: ocel::Secret,
}

#[derive(ocel::Env, Debug)]
struct Scoped {
    #[ocel(key = "FLAG", folders = ["/apps/web", "/apps/api"])]
    flag: bool,
    #[ocel(key = "REGION", folders = ["/apps/web", "/apps/api"])]
    region: String,
}

#[derive(ocel::Env, Debug)]
struct Toggles {
    toggle: bool,
    spare: Option<bool>,
}

#[test]
fn a_delivered_value_is_read_before_the_plain_one() {
    let _env = env();
    clear(&["DATABASE_NAME", "PORT", "TIMEOUT"]);
    std::env::set_var("DATABASE_NAME", "plain");
    std::env::set_var("OCEL_VAR_DATABASE_NAME", "delivered");

    let loaded = Basics::load().expect("the struct loads");

    assert_eq!(loaded.database_name, "delivered");
}

#[test]
fn a_default_stands_in_for_an_unset_value_and_an_option_stays_none() {
    let _env = env();
    clear(&["DATABASE_NAME", "PORT", "TIMEOUT"]);
    std::env::set_var("DATABASE_NAME", "shop");

    let loaded = Basics::load().expect("the struct loads");

    assert_eq!(loaded.port, 3000);
    assert_eq!(loaded.timeout, None);
}

#[test]
fn a_value_a_field_type_parses_arrives_as_that_type() {
    let _env = env();
    clear(&["DATABASE_NAME", "PORT", "TIMEOUT"]);
    std::env::set_var("DATABASE_NAME", "shop");
    std::env::set_var("PORT", "8080");
    std::env::set_var("TIMEOUT", "30");

    let loaded = Basics::load().expect("the struct loads");

    assert_eq!(loaded.port, 8080);
    assert_eq!(loaded.timeout, Some(30));
}

#[test]
fn a_required_variable_with_no_value_names_the_command_that_sets_one() {
    let _env = env();
    clear(&["DATABASE_NAME", "PORT", "TIMEOUT"]);

    let err = Basics::load().expect_err("nothing stands for DATABASE_NAME");

    assert_eq!(
        err.to_string(),
        "'DATABASE_NAME' has no value. Set one with `ocel env set DATABASE_NAME=<VALUE>`."
    );
    assert!(matches!(err, ocel::Error::Unset { .. }));
}

#[test]
fn a_plain_value_its_type_rejects_is_reported_with_the_parse_message() {
    let _env = env();
    clear(&["DATABASE_NAME", "PORT", "TIMEOUT"]);
    std::env::set_var("DATABASE_NAME", "shop");
    std::env::set_var("PORT", "eighty");

    let err = Basics::load().expect_err("eighty is not a port");

    assert_eq!(
        err.to_string(),
        "'PORT' is set but does not satisfy its type: invalid digit found in string. Fix it with `ocel env set PORT=<VALUE>`."
    );
    assert!(matches!(err, ocel::Error::Invalid { .. }));
}

#[test]
fn a_confidential_value_its_type_rejects_is_reported_without_the_parse_message() {
    let _env = env();
    clear(&["API_KEY", "SIGNING_KEY"]);
    std::env::set_var("API_KEY", "s3cret-and-unparseable");
    std::env::set_var("SIGNING_KEY", "live");

    let err = Confidential::load().expect_err("the api key is not a number");

    assert!(
        !err.to_string().contains("s3cret"),
        "error = {err}, want the value withheld"
    );
    assert_eq!(
        err.to_string(),
        "'API_KEY' is set but does not satisfy its type: withheld, because a 'sensitive' value's parse message can quote the value itself. Fix it with `ocel env set API_KEY=<VALUE>`."
    );
}

#[test]
fn a_secret_resolves_on_every_read_and_prints_a_redaction() {
    let _env = env();
    clear(&["API_KEY", "SIGNING_KEY"]);
    std::env::set_var("API_KEY", "1");
    std::env::set_var("SIGNING_KEY", "first");

    let loaded = Confidential::load().expect("the struct loads");
    assert_eq!(loaded.api_key, 1);
    assert_eq!(loaded.signing_key.key(), "SIGNING_KEY");
    assert_eq!(loaded.signing_key.value().expect("a value"), "first");

    std::env::set_var("SIGNING_KEY", "rotated");
    assert_eq!(loaded.signing_key.value().expect("a value"), "rotated");

    assert_eq!(format!("{}", loaded.signing_key), "Secret(SIGNING_KEY)");
    assert_eq!(format!("{:?}", loaded.signing_key), "Secret(SIGNING_KEY)");

    std::env::remove_var("SIGNING_KEY");
    let err = loaded.signing_key.value().expect_err("the secret is gone");
    assert_eq!(
        err.to_string(),
        "'SIGNING_KEY' has no value. Set one with `ocel env set SIGNING_KEY=<VALUE>`."
    );
}

#[test]
fn a_secret_that_was_never_delivered_fails_the_load() {
    let _env = env();
    clear(&["API_KEY", "SIGNING_KEY"]);
    std::env::set_var("API_KEY", "1");

    let err = Confidential::load().expect_err("no secret was delivered");

    assert!(matches!(err, ocel::Error::Unset { .. }));
}

#[test]
fn a_variable_scoped_past_the_folder_this_app_is_bound_to_names_both() {
    let _env = env();
    clear(&["FLAG"]);
    std::env::set_var("FLAG", "true");
    std::env::set_var("OCEL_APP_FOLDER", "/apps/admin");

    let err = Scoped::load().expect_err("this app is out of scope");

    assert_eq!(
        err.to_string(),
        "'FLAG' is scoped to /apps/web, /apps/api, but this app is bound to /apps/admin. Bind this app to one of those folders in ocel.config.ts, or widen the variable's scope."
    );
    assert!(matches!(err, ocel::Error::Scope { .. }));

    std::env::remove_var("OCEL_APP_FOLDER");
    let err = Scoped::load().expect_err("the project root is out of scope");
    assert!(
        err.to_string().contains("bound to the project root"),
        "error = {err}, want the root named"
    );
}

#[test]
fn a_variable_scoped_to_the_folder_this_app_is_bound_to_loads() {
    let _env = env();
    clear(&["FLAG", "REGION"]);
    std::env::set_var("FLAG", "true");
    std::env::set_var("REGION", "eu-west-1");
    std::env::set_var("OCEL_APP_FOLDER", "/apps/web");

    let loaded = Scoped::load().expect("this app is in scope");

    assert!(loaded.flag);
    assert_eq!(loaded.region, "eu-west-1");
    std::env::remove_var("OCEL_APP_FOLDER");
}

#[test]
fn a_bool_takes_every_form_the_other_sdks_take() {
    let _env = env();
    for raw in ["1", "t", "T", "TRUE", "true", "True"] {
        clear(&["TOGGLE", "SPARE"]);
        std::env::set_var("TOGGLE", raw);
        std::env::set_var("SPARE", raw);
        let loaded = Toggles::load().unwrap_or_else(|err| panic!("{raw}: {err}"));
        assert!(loaded.toggle, "{raw} did not load as true");
        assert_eq!(loaded.spare, Some(true), "{raw} did not load as true");
    }
    for raw in ["0", "f", "F", "FALSE", "false", "False"] {
        clear(&["TOGGLE", "SPARE"]);
        std::env::set_var("TOGGLE", raw);
        let loaded = Toggles::load().unwrap_or_else(|err| panic!("{raw}: {err}"));
        assert!(!loaded.toggle, "{raw} did not load as false");
        assert_eq!(loaded.spare, None);
    }
}

#[test]
fn a_value_no_sdk_reads_as_a_bool_is_refused_with_the_forms_that_are_read() {
    let _env = env();
    for raw in ["yes", "no", "on", "off", "2", ""] {
        clear(&["TOGGLE", "SPARE"]);
        std::env::set_var("TOGGLE", raw);
        let err = Toggles::load().expect_err("a value no sdk reads as a bool");
        assert_eq!(
            err.to_string(),
            "'TOGGLE' is set but does not satisfy its type: want one of 1 t T TRUE true True 0 f F FALSE false False. Fix it with `ocel env set TOGGLE=<VALUE>`."
        );
    }
}

#[test]
fn the_deployment_url_is_read_from_what_ocel_delivered() {
    let _env = env();
    clear(&["OCEL_URL"]);
    std::env::set_var("OCEL_URL", "https://plain.example");
    std::env::set_var("OCEL_VAR_OCEL_URL", "https://delivered.example");

    assert_eq!(
        ocel::deployment_url().expect("a url"),
        "https://delivered.example"
    );

    clear(&["OCEL_URL"]);
    let err = ocel::deployment_url().expect_err("no url was delivered");
    assert!(
        err.to_string().contains("domains.production"),
        "error = {err}, want it to name the fix"
    );
}
