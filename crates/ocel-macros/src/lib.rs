//! The attribute macros of the [`ocel`](https://docs.rs/ocel) crate. Import them from
//! there rather than depending on this crate directly.

mod attribute;
mod env;
mod kv;
mod options;
mod resources;
mod source;
mod task;
mod variable;

use proc_macro::TokenStream;
use quote::quote;
use syn::{parse_macro_input, DeriveInput, ItemFn, ReturnType, Type};

fn refuse_generics(input: &DeriveInput, derive: &str, because: &str) -> syn::Result<()> {
    match input.generics.params.is_empty() {
        true => Ok(()),
        false => Err(syn::Error::new_spanned(
            &input.generics,
            format!("a #[derive({derive})] struct takes no generic parameters, because {because}."),
        )),
    }
}

const REGISTERED: &str = "its declarations register once per binary";
const FIXED: &str = "what it declares is fixed once per binary";

/// Declare every `ocel::Postgres`, `ocel::Bucket`, `ocel::Topic<T>`, `ocel::Worker` and
/// `ocel::Kv` field of a struct, and write the `load` that hands the struct back with a
/// handle in each field.
///
/// ```ignore
/// #[derive(ocel::Resources, Clone)]
/// pub struct Infra {
///     #[ocel(name = "main", version = "17")]
///     pub db: ocel::Postgres,
///     pub cache: ocel::Postgres,
///     #[ocel(name = "avatars", public, allowed_origins = ["https://shop.example"])]
///     pub avatars: ocel::Bucket,
///     #[ocel(ordered, retry(max_attempts = 5))]
///     pub orders: ocel::Topic<Order>,
///     #[ocel(concurrency = 4)]
///     pub media: ocel::Worker,
///     #[ocel(eviction = "allkeys-lru", memory = "256mb", entries = [Requests, SessionKey])]
///     pub sessions: ocel::Kv,
/// }
/// ```
///
/// A field's name defaults to its identifier, a postgres field's version to `17`, and a
/// bucket is private and takes no browser origin unless it says otherwise. A kv store's
/// `entries` are the [`KvKey`](macro@KvKey) types whose keys it holds, and it leaves its
/// version and memory to the provider and evicts nothing unless it says otherwise. Every
/// field is one of the five, and anything else is a compile error naming the field. Each topic field
/// also gets an `ocel::TopicName<T>` constant on the struct, named after the field in upper
/// case (`Infra::ORDERS` above), which `#[ocel::consumer]` and `#[ocel::batch_consumer]`
/// name their topic by.
#[proc_macro_derive(Resources, attributes(ocel))]
pub fn resources(item: TokenStream) -> TokenStream {
    let input = parse_macro_input!(item as DeriveInput);
    refuse_generics(&input, "ocel::Resources", REGISTERED)
        .and_then(|()| resources::derive(&input))
        .unwrap_or_else(syn::Error::into_compile_error)
        .into()
}

/// Make a struct the key of one entry of an `ocel::Kv` store: its fields are the parameters
/// of the entry's pattern, checked against the pattern when the app builds.
///
/// ```ignore
/// #[derive(ocel::KvKey)]
/// #[ocel(pattern = "session/:id", json = Session, ttl = "30d")]
/// pub struct SessionKey {
///     pub id: String,
/// }
/// ```
///
/// The attribute names the pattern, `/`-separated segments that are each a literal or a
/// `:parameter`, and one shape: `text`, `counter`, `json = <TYPE>`, `list` or `set`. `ttl` is
/// how long a key lives after each write, `on_invalid = "miss"` makes a json value that does
/// not decode read as `None`, and `name` names the entry, the struct's name in snake case by
/// default. A field the pattern lacks, or a parameter no field holds, is a compile error. A
/// field is a string or an integer, written into the key percent-encoded.
#[proc_macro_derive(KvKey, attributes(ocel))]
pub fn kv_key(item: TokenStream) -> TokenStream {
    let input = parse_macro_input!(item as DeriveInput);
    refuse_generics(&input, "ocel::KvKey", FIXED)
        .and_then(|()| kv::derive(&input))
        .unwrap_or_else(syn::Error::into_compile_error)
        .into()
}

/// Declare every field of a struct as an environment variable, and write the `load` that
/// reads the delivered values into it.
///
/// ```ignore
/// #[derive(ocel::Env, Clone)]
/// pub struct Env {
///     pub database_name: String,
///     #[ocel(sensitive)]
///     pub api_key: String,
///     pub signing_key: ocel::Secret,
///     #[ocel(default = 3000)]
///     pub port: u16,
///     pub timeout: Option<u64>,
///     #[ocel(key = "FLAG", folders = ["/apps/web"])]
///     pub flag: bool,
///     /// Enable GitHub sign-in
///     #[ocel(group)]
///     pub github: Option<GitHub>,
/// }
/// ```
///
/// A field's key defaults to its identifier upper-cased. A field is required unless it
/// has a default or is an `Option`, and its value is parsed with the field type's
/// `FromStr`. A field of type `ocel::Secret` declares the secret class and resolves its
/// value on every read.
///
/// A field tagged `#[ocel(group)]` has an [`ocel::Group`](macro@Group) struct as its type,
/// whose variables are declared under a group named after the field and described by the
/// doc comment above it. An `Option` of one makes the group optional: it stays `None` until
/// a value is delivered for one of its members, and none of its members is required until
/// then. A group containing groups of its own is a compile error, because a group nests one
/// level only. The field's type is read as it is written, so a type alias for an `Option`
/// declares no optional group: spell the `Option` on the field.
#[proc_macro_derive(Env, attributes(ocel))]
pub fn env(item: TokenStream) -> TokenStream {
    let input = parse_macro_input!(item as DeriveInput);
    refuse_generics(&input, "ocel::Env", REGISTERED)
        .and_then(|()| env::derive(&input, env::Kind::Env))
        .unwrap_or_else(syn::Error::into_compile_error)
        .into()
}

/// Declare every field of a struct as a member of the group named after the
/// `#[ocel(group)]` field whose type it is, and write the `load` that reads the delivered
/// values into it.
///
/// ```ignore
/// #[derive(ocel::Env, Clone)]
/// pub struct Env {
///     /// Enable GitHub sign-in
///     #[ocel(group)]
///     pub github: Option<GitHub>,
/// }
///
/// #[derive(ocel::Group, Clone)]
/// pub struct GitHub {
///     pub client_id: String,
///     pub client_secret: ocel::Secret,
/// }
/// ```
///
/// Fields are spelled as they are on an [`ocel::Env`](macro@Env) struct. What differs is
/// that nothing is declared until a field of an `ocel::Env` struct has the group as its
/// type: a struct that no `ocel::Env` field has as its type declares no variables of its own.
#[proc_macro_derive(Group, attributes(ocel))]
pub fn group(item: TokenStream) -> TokenStream {
    let input = parse_macro_input!(item as DeriveInput);
    refuse_generics(&input, "ocel::Group", FIXED)
        .and_then(|()| env::derive(&input, env::Kind::Group))
        .unwrap_or_else(syn::Error::into_compile_error)
        .into()
}

/// Run discovery before the app does anything, and return from `main` once discovery has
/// posted what the binary declares.
///
/// ```ignore
/// #[ocel::main]
/// fn main() {
///     let _ = &infra::DB;
/// }
/// ```
///
/// A `main` that returns a `Result` propagates the discovery error with `?`, so its error
/// type has to be one `ocel::Error` converts into. A `main` returning anything other than
/// `()` or a `Result` is a compile error. It composes with a runtime's own attribute in
/// either order, so `#[tokio::main]` may sit above it or below it.
#[proc_macro_attribute]
pub fn main(_attribute: TokenStream, item: TokenStream) -> TokenStream {
    let mut function = parse_macro_input!(item as ItemFn);
    let discovery: syn::Stmt = match classify_return(&function.sig.output) {
        Some(ReturnShape::Unit) => syn::parse_quote! {
            if ::ocel::discover().expect("ocel discovery") {
                return;
            }
        },
        Some(ReturnShape::Result) => syn::parse_quote! {
            if ::ocel::discover()? {
                return Ok(());
            }
        },
        None => {
            return quote! {
                ::core::compile_error!("#[ocel::main] supports fn main() and fn main() -> Result<_, _>");
                #function
            }
            .into()
        }
    };
    function.block.stmts.insert(0, discovery);
    quote!(#function).into()
}

/// Declare an async function as a task, and replace it with an `ocel::Task<P, R>` handle
/// of the same name that triggers it.
///
/// ```ignore
/// #[ocel::task(
///     name = "resize-image",
///     retry(max_attempts = 5, min_delay = "1s", max_delay = "1m"),
///     concurrency = 10,
///     max_duration = "5m",
///     ttl = "1h",
///     worker = "media",
///     on_success = record_size,
/// )]
/// async fn resize_image(image: Image, run: &ocel::Run) -> Result<Resized, ocel::RunError> {
///     resize(&image).await
/// }
///
/// async fn record_size(image: &Image, resized: &Resized, run: &ocel::Run) -> Result<(), ocel::RunError> {
///     Ok(())
/// }
///
/// let run = resize_image.trigger(image).await?;
/// ```
///
/// The function is `async fn(payload: P, run: &ocel::Run) -> Result<R, ocel::RunError>`,
/// where `P` deserializes and serializes with serde and `R` serializes. Its future is
/// `Send`. The handle is named after the function and keeps its visibility, and the task's
/// name defaults to the function's name with `_` written as `-`.
///
/// Every option is left to the CLI's default unless it is written:
///
/// - `name = "<NAME>"`: the task's name.
/// - `retry(max_attempts = <NUMBER>, min_delay = "<DURATION>", max_delay = "<DURATION>")`.
/// - `concurrency = <NUMBER>`: the most runs at once.
/// - `max_duration = "<DURATION>"`: how long one attempt may run.
/// - `ttl = "<DURATION>"`: how long a due run waits to start before it expires.
/// - `ordered`: runs sharing a trigger's `key` start one at a time, in order.
/// - `batch(size = <NUMBER>, timeout = "<DURATION>")`: the function takes `Vec<P>` of at
///   most `size` payloads, and each trigger still sends one `P`. `size` is required.
/// - `worker = "<WORKER>"`: the worker that runs the task, `worker` unless named.
/// - `cron = "<CRON>"`: trigger the task on a schedule.
/// - `schema`: declare the JSON Schema of `P`, which needs the `schemars` feature and a
///   `schemars::JsonSchema` derive on `P`.
///
/// A duration is a whole number followed by `ms`, `s`, `m`, `h` or `d`. Hooks name an async
/// function by its path, and each is called with these arguments:
///
/// - `on_start_attempt`: `(&P, &ocel::Run) -> Result<(), ocel::RunError>`, before every
///   attempt; an error fails the attempt.
/// - `middleware`: `(&P, &ocel::Run, ocel::Next<'_, R>) -> Result<R, ocel::RunError>`,
///   around every attempt; awaiting `next` runs the function.
/// - `catch_error`: `(&P, ocel::RunError, &ocel::Run) -> ocel::RunError`, on an attempt's
///   error that is not an abort; answer with `error.into_abort()` to skip the attempts left.
/// - `on_success`: `(&P, &R, &ocel::Run) -> Result<(), ocel::RunError>`, once a run succeeds.
/// - `on_failure`: `(&P, &ocel::RunError, &ocel::Run) -> Result<(), ocel::RunError>`, once
///   a run fails with no attempt left, or aborts.
/// - `on_complete`: `(&P, Result<&R, &ocel::RunError>, &ocel::Run) -> Result<(),
///   ocel::RunError>`, after `on_success` or `on_failure`.
/// - `on_cancel`: `(&P, &ocel::Run) -> Result<(), ocel::RunError>`, when the run is
///   canceled while an attempt runs. A canceled attempt runs neither `on_success`,
///   `on_failure` nor `on_complete`.
///
/// A task with a hook hands the function a clone of the payload its hooks read, so its `P`
/// is `Clone`. With `batch(...)`, every hook's `P` is the `Vec<P>`. An error from
/// `on_success`, `on_failure`, `on_complete` or `on_cancel` is written to stderr and
/// changes nothing else.
#[proc_macro_attribute]
pub fn task(attribute: TokenStream, item: TokenStream) -> TokenStream {
    let function = parse_macro_input!(item as ItemFn);
    task::expand_task(attribute.into(), function)
        .unwrap_or_else(syn::Error::into_compile_error)
        .into()
}

/// Declare an async function as a consumer of a topic: every message sent to the topic
/// reaches it once.
///
/// ```ignore
/// #[derive(ocel::Resources)]
/// struct Infra {
///     orders: ocel::Topic<Order>,
/// }
///
/// #[ocel::consumer(topic = Infra::ORDERS, retry(max_attempts = 5), lanes = ["high"])]
/// async fn send_receipt(order: Order, run: &ocel::Run) -> Result<(), ocel::RunError> {
///     mail(&order).await
/// }
/// ```
///
/// The function is `async fn(payload: T, run: &ocel::Run) -> Result<(), ocel::RunError>`,
/// where `T` deserializes with serde, and it stays callable as it is. `topic` is required,
/// and names the topic by the `ocel::TopicName<T>` constant the struct deriving
/// `ocel::Resources` holds for its `ocel::Topic<T>` field, named after the field in upper
/// case; a consumer whose `T` is not the topic's does not compile. The consumer's name
/// defaults to the function's name with `_` written as `-`. `name`, `retry(...)`,
/// `concurrency`, `max_duration` and `worker` are written as on [`macro@task`], and
/// `lanes = ["high", "default", "low"]` limits the lanes it reads.
#[proc_macro_attribute]
pub fn consumer(attribute: TokenStream, item: TokenStream) -> TokenStream {
    let function = parse_macro_input!(item as ItemFn);
    task::expand_consumer(attribute.into(), function)
        .unwrap_or_else(syn::Error::into_compile_error)
        .into()
}

/// Declare an async function as a consumer of a topic that is handed the topic's messages
/// in batches.
///
/// ```ignore
/// #[ocel::batch_consumer(topic = Infra::ORDERS, batch_size = 100, batch_timeout = "5s")]
/// async fn index_orders(orders: Vec<Order>, run: &ocel::Run) -> Result<(), ocel::RunError> {
///     search.index(&orders).await
/// }
/// ```
///
/// The function is `async fn(payloads: Vec<T>, run: &ocel::Run) -> Result<(),
/// ocel::RunError>`. `batch_size = <NUMBER>` is required and is the most messages a batch
/// holds; `batch_timeout = "<DURATION>"` is how long a batch waits to fill before it is
/// handed over short. Every other option is written as on [`macro@consumer`].
#[proc_macro_attribute]
pub fn batch_consumer(attribute: TokenStream, item: TokenStream) -> TokenStream {
    let function = parse_macro_input!(item as ItemFn);
    task::expand_batch_consumer(attribute.into(), function)
        .unwrap_or_else(syn::Error::into_compile_error)
        .into()
}

enum ReturnShape {
    Unit,
    Result,
}

fn classify_return(output: &ReturnType) -> Option<ReturnShape> {
    let ty = match output {
        ReturnType::Default => return Some(ReturnShape::Unit),
        ReturnType::Type(_, ty) => ty,
    };
    match ty.as_ref() {
        Type::Tuple(tuple) if tuple.elems.is_empty() => Some(ReturnShape::Unit),
        Type::Path(path) if path.qself.is_none() => path
            .path
            .segments
            .last()
            .filter(|segment| segment.ident == "Result")
            .map(|_| ReturnShape::Result),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::{classify_return, ReturnShape};
    use syn::ReturnType;

    fn classify(source: &str) -> Option<ReturnShape> {
        classify_return(&syn::parse_str::<ReturnType>(source).expect("a return type"))
    }

    #[test]
    fn a_main_returning_nothing_or_a_result_is_supported() {
        assert!(matches!(classify(""), Some(ReturnShape::Unit)));
        assert!(matches!(classify("-> ()"), Some(ReturnShape::Unit)));
        assert!(matches!(
            classify("-> Result<(), ocel::Error>"),
            Some(ReturnShape::Result)
        ));
        assert!(matches!(
            classify("-> std::io::Result<()>"),
            Some(ReturnShape::Result)
        ));
    }

    #[test]
    fn a_main_returning_anything_else_is_refused() {
        assert!(classify("-> u8").is_none());
        assert!(classify("-> ((), ())").is_none());
        assert!(classify("-> impl std::fmt::Debug").is_none());
        assert!(classify("-> Box<dyn std::error::Error>").is_none());
    }
}
