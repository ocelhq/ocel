//! The attribute macros of the [`ocel`](https://docs.rs/ocel) crate. Import them from
//! there rather than depending on this crate directly.

use proc_macro::TokenStream;
use quote::quote;
use syn::{parse_macro_input, ItemFn, ReturnType, Type};

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
