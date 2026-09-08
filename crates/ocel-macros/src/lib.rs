//! The attribute macros of the [`ocel`](https://docs.rs/ocel) crate. Import them from
//! there rather than depending on this crate directly.

use proc_macro::TokenStream;
use quote::quote;
use syn::{parse_macro_input, ItemFn, ReturnType};

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
/// type has to be one `ocel::Error` converts into. Compose it with a runtime's own
/// attribute by writing `#[ocel::main]` outermost.
#[proc_macro_attribute]
pub fn main(_attribute: TokenStream, item: TokenStream) -> TokenStream {
    let mut function = parse_macro_input!(item as ItemFn);
    let discovery: syn::Stmt = match function.sig.output {
        ReturnType::Default => syn::parse_quote! {
            if ::ocel::discover().expect("ocel discovery") {
                return;
            }
        },
        ReturnType::Type(..) => syn::parse_quote! {
            if ::ocel::discover()? {
                return Ok(());
            }
        },
    };
    function.block.stmts.insert(0, discovery);
    quote!(#function).into()
}
