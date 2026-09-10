//! The attribute macros of the [`ocel`](https://docs.rs/ocel) crate. Import them from
//! there rather than depending on this crate directly.

mod attribute;
mod env;
mod resources;
mod source;
mod variable;

use proc_macro::TokenStream;
use quote::quote;
use syn::{parse_macro_input, DeriveInput, ItemFn, ReturnType, Type};

fn ungeneric(input: &DeriveInput, derive: &str, because: &str) -> syn::Result<()> {
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

/// Declare every [`Postgres`](https://docs.rs/ocel) field of a struct, and write the
/// `load` that hands the struct back with a handle in each field.
///
/// ```ignore
/// #[derive(ocel::Resources, Clone)]
/// pub struct Infra {
///     #[ocel(name = "main", version = "17")]
///     pub db: ocel::Postgres,
///     pub cache: ocel::Postgres,
/// }
/// ```
///
/// A field's name defaults to its identifier and its version to `17`. Every field is an
/// `ocel::Postgres`, and anything else is a compile error naming the field.
#[proc_macro_derive(Resources, attributes(ocel))]
pub fn resources(item: TokenStream) -> TokenStream {
    let input = parse_macro_input!(item as DeriveInput);
    ungeneric(&input, "ocel::Resources", REGISTERED)
        .and_then(|()| resources::derive(&input))
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
/// carries a default or is an `Option`, and its value is parsed with the field type's
/// `FromStr`. A field of type `ocel::Secret` declares the secret class and resolves its
/// value on every read.
///
/// A field tagged `#[ocel(group)]` holds an [`ocel::Group`](macro@Group) struct, whose
/// variables are declared under a group named after the field and described by the doc
/// comment above it. An `Option` of one makes the group optional: it stays `None` until a
/// value is delivered for one of its members, and nothing in it is owed until then. A group
/// holding groups of its own is a compile error, because a group nests one level only. The
/// field's type is read as it is written, so a type alias standing for an `Option` declares
/// no optional group: spell the `Option` on the field.
#[proc_macro_derive(Env, attributes(ocel))]
pub fn env(item: TokenStream) -> TokenStream {
    let input = parse_macro_input!(item as DeriveInput);
    ungeneric(&input, "ocel::Env", REGISTERED)
        .and_then(|()| env::derive(&input, env::Kind::Env))
        .unwrap_or_else(syn::Error::into_compile_error)
        .into()
}

/// Declare every field of a struct as a member of the group that holds it, and write the
/// `load` that reads the delivered values into it.
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
/// that nothing is declared until an `ocel::Env` struct holds the group: a struct nothing
/// holds declares no variables of its own.
#[proc_macro_derive(Group, attributes(ocel))]
pub fn group(item: TokenStream) -> TokenStream {
    let input = parse_macro_input!(item as DeriveInput);
    ungeneric(&input, "ocel::Group", FIXED)
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
    let discovery: syn::Stmt = match returns(&function.sig.output) {
        Some(Returns::Unit) => syn::parse_quote! {
            if ::ocel::discover().expect("ocel discovery") {
                return;
            }
        },
        Some(Returns::Result) => syn::parse_quote! {
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

enum Returns {
    Unit,
    Result,
}

fn returns(output: &ReturnType) -> Option<Returns> {
    let ty = match output {
        ReturnType::Default => return Some(Returns::Unit),
        ReturnType::Type(_, ty) => ty,
    };
    match ty.as_ref() {
        Type::Tuple(tuple) if tuple.elems.is_empty() => Some(Returns::Unit),
        Type::Path(path) if path.qself.is_none() => path
            .path
            .segments
            .last()
            .filter(|segment| segment.ident == "Result")
            .map(|_| Returns::Result),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::{returns, Returns};
    use syn::ReturnType;

    fn classify(source: &str) -> Option<Returns> {
        returns(&syn::parse_str::<ReturnType>(source).expect("a return type"))
    }

    #[test]
    fn a_main_returning_nothing_or_a_result_is_supported() {
        assert!(matches!(classify(""), Some(Returns::Unit)));
        assert!(matches!(classify("-> ()"), Some(Returns::Unit)));
        assert!(matches!(
            classify("-> Result<(), ocel::Error>"),
            Some(Returns::Result)
        ));
        assert!(matches!(
            classify("-> std::io::Result<()>"),
            Some(Returns::Result)
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
