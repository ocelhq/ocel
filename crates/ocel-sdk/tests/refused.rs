#[test]
fn a_declaration_ocel_cannot_accept_is_refused_before_the_app_builds() {
    trybuild::TestCases::new().compile_fail("tests/ui/*.rs");
}
