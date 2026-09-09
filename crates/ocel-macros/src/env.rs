use crate::source::source;
use crate::variable::{is_bool, refused, variable, Class, Shape, Variable};
use proc_macro2::TokenStream;
use quote::quote;
use syn::{Data, DeriveInput, Fields};

pub(crate) fn derive(input: &DeriveInput) -> syn::Result<TokenStream> {
    let variables = variables(input)?;
    let ident = &input.ident;

    let declared = variables.iter().map(|variable| {
        let key = &variable.key;
        let (file, line) = source(variable.span);
        let class = class(variable.class);
        let required = variable.required();
        let folders = variable.folders.iter();
        let check = match variable.parsed() {
            Some(ty) => {
                let parsed = parsed(ty);
                quote!(::core::option::Option::Some(::ocel::check::<#parsed>))
            }
            None => quote!(::core::option::Option::None),
        };
        quote! {
            ::ocel::DeclaredVariable {
                key: #key,
                class: #class,
                required: #required,
                folders: &[#(#folders),*],
                file: #file,
                line: #line,
                check: #check,
            }
        }
    });

    let fields = variables.iter().map(|variable| {
        let (field, key) = (&variable.ident, &variable.key);
        let class = class(variable.class);
        let folders = variable.folders.iter();
        let fallback = match &variable.fallback {
            Some(text) => quote!(::core::option::Option::Some(#text)),
            None => quote!(::core::option::Option::None),
        };
        let read = match &variable.shape {
            Shape::Live => quote!(::ocel::secret(#key, &[#(#folders),*])?),
            Shape::Optional(inner) => {
                let parsed = parsed(inner);
                let read =
                    quote!(::ocel::optional::<#parsed>(#key, #class, &[#(#folders),*], #fallback)?);
                match is_bool(inner) {
                    true => quote!(#read.map(<::core::primitive::bool as ::core::convert::From<::ocel::Boolean>>::from)),
                    false => read,
                }
            }
            Shape::Direct(ty) => {
                let parsed = parsed(ty);
                let read =
                    quote!(::ocel::value::<#parsed>(#key, #class, &[#(#folders),*], #fallback)?);
                match is_bool(ty) {
                    true => quote!(#read.into()),
                    false => read,
                }
            }
        };
        quote!(#field: #read)
    });

    Ok(quote! {
        impl ::ocel::Declare for #ident {
            fn declared() -> ::ocel::Declared {
                ::ocel::Declared {
                    resources: ::std::vec::Vec::new(),
                    variables: ::std::vec![#(#declared),*],
                }
            }

            fn load() -> ::core::result::Result<Self, ::ocel::Error> {
                ::core::result::Result::Ok(Self { #(#fields),* })
            }
        }

        impl #ident {
            /// The struct with every field read from the value delivered for it.
            pub fn load() -> ::core::result::Result<Self, ::ocel::Error> {
                <Self as ::ocel::Declare>::load()
            }
        }

        ::ocel::inventory::submit! {
            ::ocel::Registered(<#ident as ::ocel::Declare>::declared)
        }
    })
}

fn parsed(ty: &syn::Type) -> TokenStream {
    match is_bool(ty) {
        true => quote!(::ocel::Boolean),
        false => quote!(#ty),
    }
}

fn class(class: Class) -> TokenStream {
    match class {
        Class::Plain => quote!(::ocel::Class::Plain),
        Class::Sensitive => quote!(::ocel::Class::Sensitive),
        Class::Secret => quote!(::ocel::Class::Secret),
    }
}

fn variables(input: &DeriveInput) -> syn::Result<Vec<Variable>> {
    let Data::Struct(data) = &input.data else {
        return Err(syn::Error::new_spanned(
            &input.ident,
            "ocel::Env wants a struct whose fields are the variables it declares.",
        ));
    };
    let Fields::Named(named) = &data.fields else {
        return Err(syn::Error::new_spanned(
            &input.ident,
            "an ocel::Env struct has named fields.",
        ));
    };

    let mut variables: Vec<Variable> = Vec::new();
    for field in &named.named {
        let one = variable(field)?;
        if variables.iter().any(|seen| seen.key == one.key) {
            return Err(refused(
                one.span,
                &one.key,
                "is declared by two fields of the same struct. A key is declared by exactly one field.",
            ));
        }
        variables.push(one);
    }
    Ok(variables)
}
