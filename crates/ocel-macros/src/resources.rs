use crate::attribute::{parse_entries, Entry};
use crate::options::{parse_count, parse_retry, quote_none};
use crate::source::source;
use proc_macro2::TokenStream;
use quote::{format_ident, quote, quote_spanned};
use syn::spanned::Spanned;
use syn::{Data, DeriveInput, Fields, GenericArgument, PathArguments, Type};

const DEFAULT_VERSION: &str = "17";

enum Config {
    Postgres {
        version: String,
    },
    Bucket {
        public: bool,
        allowed_origins: Vec<String>,
    },
    Topic {
        payload: Type,
        schema: TokenStream,
        ordered: bool,
        retry: TokenStream,
    },
    Worker {
        concurrency: i32,
        on_start: Option<syn::Path>,
        middleware: Option<syn::Path>,
    },
}

struct Resource {
    ident: syn::Ident,
    visibility: syn::Visibility,
    name: String,
    config: Config,
    file: String,
    line: u32,
}

pub(crate) fn derive(input: &DeriveInput) -> syn::Result<TokenStream> {
    let resources = read_resources(input)?;
    let ident = &input.ident;

    let mut hooks = Vec::new();
    let declared: Vec<TokenStream> = resources
        .iter()
        .map(|resource| {
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
                Config::Topic {
                    schema,
                    ordered,
                    retry,
                    ..
                } => quote! {
                    ::ocel::DeclaredConfig::Topic {
                        schema: #schema,
                        ordered: #ordered,
                        retry: #retry,
                    }
                },
                Config::Worker {
                    concurrency,
                    on_start,
                    middleware,
                } => {
                    let on_start = match on_start {
                        Some(path) => {
                            let wrapper = format_ident!("__ocel_{}_on_start", resource.ident);
                            let call = quote_spanned!(path.span()=> ::std::boxed::Box::pin(#path()));
                            hooks.push(quote! {
                                fn #wrapper() -> ::ocel::BoxFuture<'static, ::core::result::Result<(), ::ocel::RunError>> {
                                    #call
                                }
                            });
                            quote!(::core::option::Option::Some(#wrapper))
                        }
                        None => quote_none(),
                    };
                    let middleware = match middleware {
                        Some(path) => {
                            let wrapper = format_ident!("__ocel_{}_middleware", resource.ident);
                            let call = quote_spanned!(path.span()=> ::std::boxed::Box::pin(#path(run, next)));
                            hooks.push(quote! {
                                fn #wrapper<'a>(
                                    run: &'a ::ocel::Run,
                                    next: ::ocel::Next<'a>,
                                ) -> ::ocel::BoxFuture<'a, ::core::result::Result<(), ::ocel::RunError>> {
                                    #call
                                }
                            });
                            quote!(::core::option::Option::Some(#wrapper))
                        }
                        None => quote_none(),
                    };
                    quote! {
                        ::ocel::DeclaredConfig::Worker {
                            concurrency: #concurrency,
                            on_start: #on_start,
                            middleware: #middleware,
                        }
                    }
                }
            };
            quote! {
                ::ocel::DeclaredResource { name: #name, config: #config, file: #file, line: #line }
            }
        })
        .collect();
    let fields = resources.iter().map(|resource| {
        let (field, name) = (&resource.ident, &resource.name);
        match &resource.config {
            Config::Postgres { .. } => quote!(#field: ::ocel::Postgres::new(#name)),
            Config::Bucket { .. } => quote!(#field: ::ocel::Bucket::new(#name)),
            Config::Topic { payload, .. } => {
                quote!(#field: ::ocel::Topic::<#payload>::new(#name))
            }
            Config::Worker { .. } => quote!(#field: ::ocel::Worker::new(#name)),
        }
    });

    let topic_names = resources.iter().filter_map(|resource| {
        let Config::Topic { payload, .. } = &resource.config else {
            return None;
        };
        let constant = format_ident!(
            "{}",
            resource
                .ident
                .to_string()
                .trim_start_matches("r#")
                .to_uppercase(),
            span = resource.ident.span()
        );
        let (visibility, name) = (&resource.visibility, &resource.name);
        let doc = format!(
            "The `{name}` topic's name, which `#[ocel::consumer(topic = {ident}::{constant})]` consumes."
        );
        Some(quote! {
            #[doc = #doc]
            #visibility const #constant: ::ocel::TopicName<#payload> = ::ocel::TopicName::new(#name);
        })
    });

    Ok(quote! {
        impl ::ocel::Declare for #ident {
            fn declared() -> ::ocel::Declared {
                #(#hooks)*

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

            #(#topic_names)*
        }

        ::ocel::inventory::submit! {
            ::ocel::Registered(<#ident as ::ocel::Declare>::declared)
        }
    })
}

fn read_resources(input: &DeriveInput) -> syn::Result<Vec<Resource>> {
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
        let Some(kind) = find_kind(&field.ty) else {
            return Err(syn::Error::new_spanned(
                &field.ty,
                format!(
                    "field {ident} is not an ocel::Postgres, an ocel::Bucket, an ocel::Topic<T> or an ocel::Worker, and every field of an ocel::Resources struct declares one."
                ),
            ));
        };

        let mut name = ident.to_string();
        let mut config = match &kind {
            Kind::Postgres => Config::Postgres {
                version: DEFAULT_VERSION.to_string(),
            },
            Kind::Bucket => Config::Bucket {
                public: false,
                allowed_origins: Vec::new(),
            },
            Kind::Topic(payload) => Config::Topic {
                payload: (**payload).clone(),
                schema: quote_none(),
                ordered: false,
                retry: quote_none(),
            },
            Kind::Worker => Config::Worker {
                concurrency: 0,
                on_start: None,
                middleware: None,
            },
        };
        for entry in parse_entries(&field.attrs)? {
            read_entry(&ident, &kind, &mut name, &mut config, &entry).map_err(|err| {
                match err.to_string().starts_with("field ") {
                    true => err,
                    false => syn::Error::new(err.span(), format!("field {ident}: {err}")),
                }
            })?;
        }
        if let Some(seen) = resources.iter().find(|seen| {
            seen.name == name && read_namespace(&seen.config) == read_namespace(&config)
        }) {
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
            visibility: field.vis.clone(),
            name,
            config,
            file,
            line,
        });
    }
    Ok(resources)
}

fn read_entry(
    ident: &syn::Ident,
    kind: &Kind,
    name: &mut String,
    config: &mut Config,
    entry: &Entry,
) -> syn::Result<()> {
    match (config, entry.name.to_string().as_str()) {
        (_, "name") => *name = entry.read_literal()?.to_string(),
        (Config::Postgres { version }, "version") => *version = entry.read_literal()?.to_string(),
        (Config::Bucket { public, .. }, "public") => *public = true,
        (
            Config::Bucket {
                allowed_origins, ..
            },
            "allowed_origins",
        ) => *allowed_origins = entry.read_list()?.to_vec(),
        (Config::Topic { ordered, .. }, "ordered") => {
            entry.expect_flag()?;
            *ordered = true;
        }
        (Config::Topic { retry: policy, .. }, "retry") => *policy = parse_retry(entry)?,
        (
            Config::Topic {
                schema, payload, ..
            },
            "schema",
        ) => *schema = quote_topic_schema(entry, payload)?,
        (Config::Worker { concurrency, .. }, "concurrency") => *concurrency = parse_count(entry)?,
        (Config::Worker { on_start, .. }, "on_start") => {
            *on_start = Some(entry.read_path()?.clone())
        }
        (Config::Worker { middleware, .. }, "middleware") => {
            *middleware = Some(entry.read_path()?.clone())
        }
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
    Ok(())
}

fn quote_topic_schema(entry: &Entry, payload: &Type) -> syn::Result<TokenStream> {
    entry.expect_flag()?;
    Ok(quote_spanned!(entry.span()=> ::ocel::__json_schema!(#payload)))
}

fn read_namespace(config: &Config) -> &'static str {
    match config {
        Config::Postgres { .. } | Config::Bucket { .. } => "resource",
        Config::Topic { .. } => "topic",
        Config::Worker { .. } => "worker",
    }
}

enum Kind {
    Postgres,
    Bucket,
    Topic(Box<Type>),
    Worker,
}

impl Kind {
    fn spelling(&self) -> &'static str {
        match self {
            Self::Postgres => "an ocel::Postgres",
            Self::Bucket => "an ocel::Bucket",
            Self::Topic(_) => "an ocel::Topic",
            Self::Worker => "an ocel::Worker",
        }
    }

    fn attributes(&self) -> &'static str {
        match self {
            Self::Postgres => "name = \"<NAME>\" and version = \"<VERSION>\"",
            Self::Bucket => "name = \"<NAME>\", public and allowed_origins = [\"<ORIGIN>\"]",
            Self::Topic(_) => "name = \"<NAME>\", ordered, retry(...) and schema",
            Self::Worker => {
                "name = \"<NAME>\", concurrency = <NUMBER>, on_start = <PATH> and middleware = <PATH>"
            }
        }
    }
}

fn find_kind(field_type: &Type) -> Option<Kind> {
    let Type::Path(path) = field_type else {
        return None;
    };
    if path.qself.is_some() {
        return None;
    }
    let last = path.path.segments.last()?;
    match last.ident.to_string().as_str() {
        "Postgres" => Some(Kind::Postgres),
        "Bucket" => Some(Kind::Bucket),
        "Worker" => Some(Kind::Worker),
        "Topic" => {
            let PathArguments::AngleBracketed(arguments) = &last.arguments else {
                return None;
            };
            match arguments.args.first()? {
                GenericArgument::Type(payload) => Some(Kind::Topic(Box::new(payload.clone()))),
                _ => None,
            }
        }
        _ => None,
    }
}
