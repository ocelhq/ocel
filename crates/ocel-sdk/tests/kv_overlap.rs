#[allow(dead_code)]
#[derive(ocel::KvKey)]
#[ocel(pattern = "session/:id", text)]
struct Session {
    id: String,
}

#[allow(dead_code)]
#[derive(ocel::KvKey)]
#[ocel(pattern = "session/current", text)]
struct Current;

#[allow(dead_code)]
#[derive(ocel::Resources)]
struct Infra {
    #[ocel(entries = [Session, Current])]
    cache: ocel::Kv,
}

#[test]
fn two_entries_of_a_store_whose_patterns_overlap_fail_discovery_naming_both_lines() {
    std::env::set_var("OCEL_PHASE", "discovery");

    let err = ocel::discover().expect_err("overlapping patterns");

    assert!(matches!(err, ocel::Error::Definition { .. }));
    let said = err.to_string();
    for want in [
        "'cache'",
        "'current'",
        "\"session/current\"",
        "overlaps",
        "\"session/:id\"",
        "'session'",
        "kv_overlap.rs:4",
        "kv_overlap.rs:11",
    ] {
        assert!(said.contains(want), "error = {said}, want {want} in it");
    }
}
