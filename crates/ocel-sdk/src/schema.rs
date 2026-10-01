#[cfg(feature = "schemars")]
#[doc(hidden)]
pub fn generate_json_schema<P: schemars::JsonSchema>() -> String {
    serde_json::to_string(&schemars::schema_for!(P)).unwrap_or_default()
}

#[cfg(feature = "schemars")]
#[doc(hidden)]
#[macro_export]
macro_rules! __json_schema {
    ($payload:ty) => {
        ::core::option::Option::Some(
            ::ocel::generate_json_schema::<$payload> as fn() -> ::std::string::String,
        )
    };
}

#[cfg(not(feature = "schemars"))]
#[doc(hidden)]
#[macro_export]
macro_rules! __json_schema {
    ($payload:ty) => {
        ::core::compile_error!(
            "`schema` declares the JSON Schema of the payload, which ocel derives with schemars: enable the `schemars` feature of the ocel-sdk dependency, and derive schemars::JsonSchema on the payload type"
        )
    };
}
