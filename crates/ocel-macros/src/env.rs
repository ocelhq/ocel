use crate::attribute::{parse_entries, read_container_crate_path};
use crate::source::source;
use crate::variable::{description, is_bool, refused, variable, Class, Shape, Variable};
use proc_macro2::{Span, TokenStream};
use quote::quote;
use syn::{Data, DeriveInput, Fields, GenericArgument, PathArguments, Type};

#[derive(Clone, Copy)]
pub(crate) enum Kind {
    Env,
    Group,
}

impl Kind {
    pub(crate) fn label(self) -> &'static str {
        match self {
            Kind::Env => "ocel::Env",
            Kind::Group => "ocel::Group",
        }
    }
}

pub(crate) fn derive(input: &DeriveInput, kind: Kind) -> syn::Result<TokenStream> {
    let label = kind.label();
    let krate = &read_container_crate_path(input, label)?;
    let variables = variables(input, label)?;
    let groups = groups(input, label)?;
    let ident = &input.ident;

    let declared = variables.iter().map(|variable| {
        let key = &variable.key;
        let (file, line) = source(variable.span);
        let class = class(variable.class, krate);
        let required = variable.required();
        let folders = variable.folders.iter();
        let description = text(variable.description.as_deref());
        let check = match variable.parsed() {
            Some(ty) => {
                let parsed = parsed(ty, krate);
                quote!(::core::option::Option::Some(#krate::check::<#parsed>))
            }
            None => quote!(::core::option::Option::None),
        };
        quote! {
            #krate::DeclaredVariable {
                key: #key,
                class: #class,
                required: #required,
                folders: &[#(#folders),*],
                description: #description,
                file: #file,
                line: #line,
                check: #check,
                group: ::core::option::Option::None,
            }
        }
    });

    let fields = variables.iter().map(|variable| {
        let (field, key) = (&variable.ident, &variable.key);
        let class = class(variable.class, krate);
        let folders = variable.folders.iter();
        let fallback = text(variable.fallback.as_deref());
        let read = match &variable.shape {
            Shape::Live => quote!(#krate::secret(#key, &[#(#folders),*])?),
            Shape::Optional(inner) => {
                let parsed = parsed(inner, krate);
                let read =
                    quote!(#krate::optional::<#parsed>(#key, #class, &[#(#folders),*], #fallback)?);
                match is_bool(inner) {
                    true => quote!(#read.map(<::core::primitive::bool as ::core::convert::From<#krate::Boolean>>::from)),
                    false => read,
                }
            }
            Shape::Direct(ty) => {
                let parsed = parsed(ty, krate);
                let read =
                    quote!(#krate::value::<#parsed>(#key, #class, &[#(#folders),*], #fallback)?);
                match is_bool(ty) {
                    true => quote!(#read.into()),
                    false => read,
                }
            }
        };
        quote!(#field: #read)
    });

    let declared_groups = groups.iter().map(|group| {
        let (key, ty, required) = (&group.key, &group.ty, !group.optional);
        let (file, line) = source(group.span);
        let description = text(group.description.as_deref());
        quote! {
            #krate::DeclaredGroup {
                key: #key,
                required: #required,
                description: #description,
                members: <#ty as #krate::Declare>::declared,
                file: #file,
                line: #line,
            }
        }
    });

    let group_fields = groups.iter().map(|group| {
        let (field, ty) = (&group.ident, &group.ty);
        match group.optional {
            true => quote! {
                #field: match #krate::group_present(
                    &<#ty as #krate::Declare>::declared().variables,
                ) {
                    true => ::core::option::Option::Some(<#ty as #krate::Declare>::load()?),
                    false => ::core::option::Option::None,
                }
            },
            false => quote!(#field: <#ty as #krate::Declare>::load()?),
        }
    });

    let checks = groups.iter().map(|group| {
        let ty = &group.ty;
        quote!(one_level::<#ty>();)
    });
    let nesting = (!groups.is_empty()).then(|| {
        quote! {
            const _: () = {
                fn one_level<T: #krate::Group>() {}
                fn nesting() {
                    #(#checks)*
                }
            };
        }
    });
    let (flat, submission) = match kind {
        Kind::Group => {
            satisfiable(ident, &variables)?;
            let flat = groups
                .is_empty()
                .then(|| quote!(impl #krate::Group for #ident {}));
            (flat, None)
        }
        Kind::Env => (
            None,
            Some(quote! {
                #krate::inventory::submit! {
                    #krate::Registered(<#ident as #krate::Declare>::declared)
                }
            }),
        ),
    };

    Ok(quote! {
        impl #krate::Declare for #ident {
            fn declared() -> #krate::Declared {
                #krate::Declared {
                    resources: ::std::vec::Vec::new(),
                    variables: ::std::vec![#(#declared),*],
                    groups: ::std::vec![#(#declared_groups),*],
                }
            }

            fn load() -> ::core::result::Result<Self, #krate::Error> {
                ::core::result::Result::Ok(Self { #(#fields,)* #(#group_fields),* })
            }
        }

        #flat

        impl #ident {
            /// The struct with every field read from the value delivered for it.
            pub fn load() -> ::core::result::Result<Self, #krate::Error> {
                <Self as #krate::Declare>::load()
            }
        }

        #nesting

        #submission
    })
}

fn satisfiable(ident: &syn::Ident, variables: &[Variable]) -> syn::Result<()> {
    let mut shared: Option<Vec<String>> = None;
    let mut scoped: Vec<String> = Vec::new();
    for variable in variables {
        if variable.folders.is_empty() {
            continue;
        }
        scoped.push(format!(
            "{} ({})",
            variable.key,
            variable.folders.join(", ")
        ));
        shared = Some(match shared {
            None => variable.folders.clone(),
            Some(common) => common
                .into_iter()
                .filter(|folder| variable.folders.contains(folder))
                .collect(),
        });
    }
    match shared {
        Some(shared) if shared.is_empty() => Err(syn::Error::new_spanned(
            ident,
            format!(
                "{ident} is read as one, and no folder satisfies every member: {}.",
                scoped.join(", ")
            ),
        )),
        _ => Ok(()),
    }
}

fn text(value: Option<&str>) -> TokenStream {
    match value {
        Some(text) => quote!(::core::option::Option::Some(#text)),
        None => quote!(::core::option::Option::None),
    }
}

fn parsed(ty: &syn::Type, krate: &syn::Path) -> TokenStream {
    match is_bool(ty) {
        true => quote!(#krate::Boolean),
        false => quote!(#ty),
    }
}

fn class(class: Class, krate: &syn::Path) -> TokenStream {
    match class {
        Class::Plain => quote!(#krate::Class::Plain),
        Class::Sensitive => quote!(#krate::Class::Sensitive),
        Class::Secret => quote!(#krate::Class::Secret),
    }
}

fn named<'a>(
    input: &'a DeriveInput,
    label: &str,
) -> syn::Result<&'a syn::punctuated::Punctuated<syn::Field, syn::Token![,]>> {
    let Data::Struct(data) = &input.data else {
        return Err(syn::Error::new_spanned(
            &input.ident,
            format!("{label} wants a struct whose fields are the variables it declares."),
        ));
    };
    let Fields::Named(named) = &data.fields else {
        return Err(syn::Error::new_spanned(
            &input.ident,
            format!("a {label} struct has named fields."),
        ));
    };
    Ok(&named.named)
}

fn grouped(field: &syn::Field) -> syn::Result<bool> {
    Ok(parse_entries(&field.attrs)?
        .iter()
        .any(|entry| entry.name == "group"))
}

fn variables(input: &DeriveInput, label: &str) -> syn::Result<Vec<Variable>> {
    let mut variables: Vec<Variable> = Vec::new();
    for field in named(input, label)? {
        if grouped(field)? {
            continue;
        }
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

struct Group {
    ident: syn::Ident,
    ty: Type,
    key: String,
    optional: bool,
    description: Option<String>,
    span: Span,
}

fn groups(input: &DeriveInput, label: &str) -> syn::Result<Vec<Group>> {
    let mut groups: Vec<Group> = Vec::new();
    for field in named(input, label)? {
        if !grouped(field)? {
            continue;
        }
        let ident = field.ident.clone().ok_or_else(|| {
            syn::Error::new_spanned(field, format!("a {label} struct has named fields."))
        })?;
        let key = ident.to_string();
        let span = ident.span();
        if parse_entries(&field.attrs)?.len() != 1 {
            return Err(refused(
                span,
                &key,
                "has #[ocel(group)] beside another attribute. A group takes only #[ocel(group)], and its description is the doc comment above it.",
            ));
        }
        let Some((ty, optional)) = group_type(&field.ty) else {
            return Err(refused(
                span,
                &key,
                "is tagged #[ocel(group)], so its type is an ocel::Group struct, or an Option of one to make the group optional.",
            ));
        };
        groups.push(Group {
            ident,
            ty,
            key,
            optional,
            description: description(field)?,
            span,
        });
    }
    Ok(groups)
}

fn group_type(ty: &Type) -> Option<(Type, bool)> {
    let Type::Path(path) = ty else {
        return None;
    };
    let segment = path.path.segments.last()?;
    if segment.ident != "Option" {
        return matches!(segment.arguments, PathArguments::None).then(|| (ty.clone(), false));
    }
    let PathArguments::AngleBracketed(arguments) = &segment.arguments else {
        return None;
    };
    arguments.args.iter().find_map(|argument| match argument {
        GenericArgument::Type(inner) => Some((inner.clone(), true)),
        _ => None,
    })
}
