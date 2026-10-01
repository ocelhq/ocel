#[test]
fn a_declaration_ocel_cannot_accept_is_refused_before_the_app_builds() {
    trybuild::TestCases::new().compile_fail("tests/ui/*.rs");
}

#[cfg(not(feature = "schemars"))]
#[test]
fn a_schema_without_the_schemars_feature_is_refused_before_the_app_builds() {
    trybuild::TestCases::new().compile_fail("tests/ui-without-schemars/*.rs");
}

#[cfg(not(feature = "realtime"))]
#[test]
fn a_channel_without_the_realtime_feature_is_refused_before_the_app_builds() {
    trybuild::TestCases::new().compile_fail("tests/ui-without-realtime/*.rs");
}
