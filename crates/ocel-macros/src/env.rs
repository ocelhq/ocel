use crate::attribute::entries;
use crate::source::source;
use crate::variable::{description, is_bool, refused, variable, Class, Shape, Variable};
use proc_macro2::{Span, TokenStream};
use quote::quote;
use syn::{Data, DeriveInput, Fields, GenericArgument, PathArguments, Type};

pub(crate) fn derive(input: &DeriveInput) -> syn::Result<TokenStream> {
    let variables = variables(input)?;
    let groups = groups(input)?;
    let ident = &input.ident;

    let declared = variables.iter().map(|variable| {
        let key = &variable.key;
        let (file, line) = source(variable.span);
        let class = class(variable.class);
        let required = variable.required();
        let folders = variable.folders.iter();
        let description = text(variable.description.as_deref());
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
        let class = class(variable.class);
        let folders = variable.folders.iter();
        let fallback = text(variable.fallback.as_deref());
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

    let declared_groups = groups.iter().map(|group| {
        let (key, ty, required) = (&group.key, &group.ty, !group.optional);
        let (file, line) = source(group.span);
        let description = text(group.description.as_deref());
        quote! {
            ::ocel::DeclaredGroup {
                key: #key,
                required: #required,
                description: #description,
                members: <#ty as ::ocel::Declare>::declared,
                file: #file,
                line: #line,
            }
        }
    });

    let group_fields = groups.iter().map(|group| {
        let (field, ty) = (&group.ident, &group.ty);
        match group.optional {
            true => quote! {
                #field: match ::ocel::group_present(
                    &<#ty as ::ocel::Declare>::declared().variables,
                ) {
                    true => ::core::option::Option::Some(<#ty as ::ocel::Declare>::load()?),
                    false => ::core::option::Option::None,
                }
            },
            false => quote!(#field: <#ty as ::ocel::Declare>::load()?),
        }
    });

    let held = groups.iter().map(|group| {
        let ty = &group.ty;
        quote!(one_level::<#ty>();)
    });
    let nesting = (!groups.is_empty()).then(|| {
        quote! {
            const _: () = {
                fn one_level<T: ::ocel::Group>() {}
                fn nesting() {
                    #(#held)*
                }
            };
        }
    });
    let flat = groups
        .is_empty()
        .then(|| quote!(impl ::ocel::Group for #ident {}));

    Ok(quote! {
        impl ::ocel::Declare for #ident {
            fn declared() -> ::ocel::Declared {
                ::ocel::Declared {
                    resources: ::std::vec::Vec::new(),
                    variables: ::std::vec![#(#declared),*],
                    groups: ::std::vec![#(#declared_groups),*],
                }
            }

            fn load() -> ::core::result::Result<Self, ::ocel::Error> {
                ::core::result::Result::Ok(Self { #(#fields,)* #(#group_fields),* })
            }
        }

        #flat

        impl #ident {
            /// The struct with every field read from the value delivered for it.
            pub fn load() -> ::core::result::Result<Self, ::ocel::Error> {
                <Self as ::ocel::Declare>::load()
            }
        }

        #nesting

        ::ocel::inventory::submit! {
            ::ocel::Registered(<#ident as ::ocel::Declare>::declared)
        }
    })
}

fn text(value: Option<&str>) -> TokenStream {
    match value {
        Some(text) => quote!(::core::option::Option::Some(#text)),
        None => quote!(::core::option::Option::None),
    }
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

fn named(
    input: &DeriveInput,
) -> syn::Result<&syn::punctuated::Punctuated<syn::Field, syn::Token![,]>> {
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
    Ok(&named.named)
}

fn grouped(field: &syn::Field) -> syn::Result<bool> {
    Ok(entries(&field.attrs)?
        .iter()
        .any(|entry| entry.name == "group"))
}

fn variables(input: &DeriveInput) -> syn::Result<Vec<Variable>> {
    let mut variables: Vec<Variable> = Vec::new();
    for field in named(input)? {
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

fn groups(input: &DeriveInput) -> syn::Result<Vec<Group>> {
    let mut groups: Vec<Group> = Vec::new();
    for field in named(input)? {
        if !grouped(field)? {
            continue;
        }
        let ident = field
            .ident
            .clone()
            .ok_or_else(|| syn::Error::new_spanned(field, "an Env struct has named fields."))?;
        let key = ident.to_string();
        let span = ident.span();
        if entries(&field.attrs)?.len() != 1 {
            return Err(refused(
                span,
                &key,
                "carries #[ocel(group)] beside another attribute. A group takes only #[ocel(group)], and its description is the doc comment above it.",
            ));
        }
        let Some((ty, optional)) = held(&field.ty) else {
            return Err(refused(
                span,
                &key,
                "is tagged #[ocel(group)], so it holds an ocel::Env struct, or an Option of one to make the group optional.",
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

fn held(ty: &Type) -> Option<(Type, bool)> {
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
