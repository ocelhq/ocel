use crate::attribute::entries;
use crate::source::source;
use proc_macro2::TokenStream;
use quote::quote;
use syn::{Data, DeriveInput, Fields, Type};

const DEFAULT_VERSION: &str = "17";

enum Config {
    Postgres {
        version: String,
    },
    Bucket {
        public: bool,
        allowed_origins: Vec<String>,
    },
}

struct Resource {
    ident: syn::Ident,
    name: String,
    config: Config,
    file: String,
    line: u32,
}

pub(crate) fn derive(input: &DeriveInput) -> syn::Result<TokenStream> {
    let resources = resources(input)?;
    let ident = &input.ident;

    let declared = resources.iter().map(|resource| {
        let name = &resource.name;
        let (file, line) = (&resource.file, resource.line);
        let config = match &resource.config {
            Config::Postgres { version } => {
                quote!(::ocel::DeclaredConfig::Postgres { version: #version })
            }
            Config::Bucket {
                public,
                allowed_origins,
            } => quote! {
                ::ocel::DeclaredConfig::Bucket {
                    public: #public,
                    allowed_origins: &[#(#allowed_origins),*],
                }
            },
        };
        quote! {
            ::ocel::DeclaredResource { name: #name, config: #config, file: #file, line: #line }
        }
    });
    let fields = resources.iter().map(|resource| {
        let (field, name) = (&resource.ident, &resource.name);
        match resource.config {
            Config::Postgres { .. } => quote!(#field: ::ocel::Postgres::new(#name)),
            Config::Bucket { .. } => quote!(#field: ::ocel::Bucket::new(#name)),
        }
    });

    Ok(quote! {
        impl ::ocel::Declare for #ident {
            fn declared() -> ::ocel::Declared {
                ::ocel::Declared {
                    resources: ::std::vec![#(#declared),*],
                    variables: ::std::vec::Vec::new(),
                    groups: ::std::vec::Vec::new(),
                }
            }

            fn load() -> ::core::result::Result<Self, ::ocel::Error> {
                ::core::result::Result::Ok(Self { #(#fields),* })
            }
        }

        impl #ident {
            /// The struct with a handle in every field, ready to read bindings through.
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
        let Some(kind) = kind(&field.ty) else {
            return Err(syn::Error::new_spanned(
                &field.ty,
                format!(
                    "field {ident} is neither an ocel::Postgres nor an ocel::Bucket, and every field of an ocel::Resources struct declares one."
                ),
            ));
        };

        let mut name = ident.to_string();
        let mut version = DEFAULT_VERSION.to_string();
        let mut public = false;
        let mut allowed_origins = Vec::new();
        for entry in entries(&field.attrs)? {
            match (kind, entry.name.to_string().as_str()) {
                (_, "name") => name = entry.literal()?.to_string(),
                (Kind::Postgres, "version") => version = entry.literal()?.to_string(),
                (Kind::Bucket, "public") => public = true,
                (Kind::Bucket, "allowed_origins") => allowed_origins = entry.list()?.to_vec(),
                (_, other) => {
                    return Err(syn::Error::new(
                        entry.span(),
                        format!(
                            "field {ident} has an unknown attribute '{other}'. The attributes of {} are {}.",
                            kind.spelling(),
                            kind.attributes(),
                        ),
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
            config: match kind {
                Kind::Postgres => Config::Postgres { version },
                Kind::Bucket => Config::Bucket {
                    public,
                    allowed_origins,
                },
            },
            file,
            line,
        });
    }
    Ok(resources)
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Kind {
    Postgres,
    Bucket,
}

impl Kind {
    fn spelling(self) -> &'static str {
        match self {
            Self::Postgres => "an ocel::Postgres",
            Self::Bucket => "an ocel::Bucket",
        }
    }

    fn attributes(self) -> &'static str {
        match self {
            Self::Postgres => "name = \"<NAME>\" and version = \"<VERSION>\"",
            Self::Bucket => "name = \"<NAME>\", public and allowed_origins = [\"<ORIGIN>\"]",
        }
    }
}

fn kind(ty: &Type) -> Option<Kind> {
    let Type::Path(path) = ty else {
        return None;
    };
    if path.qself.is_some() {
        return None;
    }
    match path.path.segments.last()?.ident.to_string().as_str() {
        "Postgres" => Some(Kind::Postgres),
        "Bucket" => Some(Kind::Bucket),
        _ => None,
    }
}
