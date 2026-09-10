#![allow(
    clippy::all,
    missing_docs,
    non_camel_case_types,
    non_snake_case,
    unused
)]

pub mod app {
    pub mod blob {
        pub mod v1 {
            include!("proto/app.blob.v1.rs");
            include!("proto/app.blob.v1.mod.rs");
        }
    }

    pub mod resources {
        pub mod v1 {
            include!("proto/app.resources.v1.rs");
            include!("proto/app.resources.v1.mod.rs");
        }
    }
}

pub mod common {
    pub mod bindings {
        pub mod v1 {
            include!("proto/common.bindings.v1.rs");
        }
    }
}
