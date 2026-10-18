# ocel-sdk

The Rust SDK of [Ocel](https://ocel.dev), which deploys apps into your own cloud account.
An app declares the infrastructure it needs in its own code, and `ocel deploy` provisions it.

```sh
cargo add ocel-sdk
```

```rust
#[derive(ocel::Resources, Clone)]
pub struct Infra {
    #[ocel(name = "main", version = "17")]
    pub db: ocel::Postgres,
    #[ocel(name = "avatars", public)]
    pub avatars: ocel::Bucket,
}
```

The [SDK docs](https://ocel.dev/docs/sdk) cover every resource, and the
[API reference](https://docs.rs/ocel-sdk) every type.
