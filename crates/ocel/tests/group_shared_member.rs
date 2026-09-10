#[allow(dead_code)]
#[derive(ocel::Env)]
struct Web {
    #[ocel(group)]
    github: Option<GitHub>,
}

#[allow(dead_code)]
#[derive(ocel::Env)]
struct Api {
    #[ocel(group)]
    auth: Option<GitHub>,
}

#[allow(dead_code)]
#[derive(ocel::Group)]
struct GitHub {
    #[ocel(key = "GITHUB_CLIENT_ID")]
    client_id: String,
}

#[test]
fn a_member_two_groups_both_claim_names_both_the_groups() {
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", "http://127.0.0.1:1");

    let err = ocel::discover().expect_err("two groups claim GITHUB_CLIENT_ID");

    assert!(matches!(err, ocel::Error::Definition { .. }));
    assert_eq!(
        err.to_string(),
        "'GITHUB_CLIENT_ID' belongs to the group 'github' and to the group 'auth'. A variable belongs to one group."
    );
}
