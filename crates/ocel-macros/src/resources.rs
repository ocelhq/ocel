use crate::attribute::entries;
use crate::source::source;
use proc_macro2::TokenStream;
use quote::quote;
use syn::{Data, DeriveInput, Fields, Type};

const DEFAULT_VERSION: &str = "17";

struct Resource {
    ident: syn::Ident,
    name: String,
    version: String,
    file: String,
    line: u32,
}

pub(crate) fn derive(input: &DeriveInput) -> syn::Result<TokenStream> {
    let resources = resources(input)?;
    let ident = &input.ident;

    let declared = resources.iter().map(|resource| {
        let (name, version) = (&resource.name, &resource.version);
        let (file, line) = (&resource.file, resource.line);
        quote! {
            ::ocel::DeclaredResource { name: #name, version: #version, file: #file, line: #line }
        }
    });
    let fields = resources.iter().map(|resource| {
        let (field, name) = (&resource.ident, &resource.name);
        quote!(#field: ::ocel::Postgres::new(#name))
    });

    Ok(quote! {
        impl ::ocel::Declare for #ident {
            fn declared() -> ::ocel::Declared {
                ::ocel::Declared {
                    resources: ::std::vec![#(#declared),*],
                    variables: ::std::vec::Vec::new(),
                }
            }

            fn load() -> ::core::result::Result<Self, ::ocel::Error> {
                ::core::result::Result::Ok(Self { #(#fields),* })
            }
        }

        impl #ident {
            /// The struct with a handle in every field, ready to read links through.
            pub fn load() -> ::core::result::Result<Self, ::ocel::Error> {
                <Self as ::ocel::Declare>::load()
            }
        }

        ::ocel::inventory::submit! {
            ::ocel::Registered(<#ident as ::ocel::Declare>::declared)
        }
    })
}

fn resources(input: &DeriveInput) -> syn::Result<Vec<Resource>> {
    let Data::Struct(data) = &input.data else {
        return Err(syn::Error::new_spanned(
            &input.ident,
            "ocel::Resources wants a struct whose fields are the resources it declares.",
        ));
    };
    let Fields::Named(named) = &data.fields else {
        return Err(syn::Error::new_spanned(
            &input.ident,
            "an ocel::Resources struct has named fields.",
        ));
    };

    let mut resources: Vec<Resource> = Vec::new();
    for field in &named.named {
        let ident = field.ident.clone().expect("a named field");
        if !is_postgres(&field.ty) {
            return Err(syn::Error::new_spanned(
                &field.ty,
                format!(
                    "field {ident} is not an ocel::Postgres, and every field of an ocel::Resources struct declares one."
                ),
            ));
        }

        let mut name = ident.to_string();
        let mut version = DEFAULT_VERSION.to_string();
        for entry in entries(&field.attrs)? {
            match entry.name.to_string().as_str() {
                "name" => name = entry.literal()?.to_string(),
                "version" => version = entry.literal()?.to_string(),
                other => {
                    return Err(syn::Error::new(
                        entry.span(),
                        format!("field {ident} has an unknown attribute '{other}'. The attributes are name = \"<NAME>\" and version = \"<VERSION>\"."),
                    ))
                }
            }
        }
        if let Some(seen) = resources.iter().find(|seen| seen.name == name) {
            return Err(syn::Error::new_spanned(
                &field.ident,
                format!(
                    "'{name}' is declared by both {} and {ident}. A name is declared by exactly one field.",
                    seen.ident
                ),
            ));
        }
        let (file, line) = source(ident.span());
        resources.push(Resource {
            ident,
            name,
            version,
            file,
            line,
        });
    }
    Ok(resources)
}

fn is_postgres(ty: &Type) -> bool {
    match ty {
        Type::Path(path) if path.qself.is_none() => path
            .path
            .segments
            .last()
            .is_some_and(|segment| segment.ident == "Postgres"),
        _ => false,
    }
}
