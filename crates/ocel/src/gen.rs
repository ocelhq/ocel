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
            include!("gen/app.blob.v1.rs");
            include!("gen/app.blob.v1.mod.rs");
        }
    }

    pub mod resources {
        pub mod v1 {
            include!("gen/app.resources.v1.rs");
            include!("gen/app.resources.v1.mod.rs");
        }
    }
}

pub mod common {
    pub mod links {
        pub mod v1 {
            include!("gen/common.links.v1.rs");
        }
    }
}
