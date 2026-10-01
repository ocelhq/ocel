use proc_macro2::Span;
use syn::parse::{Parse, ParseStream, Parser};
use syn::punctuated::Punctuated;
use syn::{Attribute, Ident, Lit, LitStr, Token};

pub(crate) enum Value {
    Flag,
    Literal(String),
    List(Vec<String>),
    Paths(Vec<syn::Path>),
    Path(syn::Path),
    Nested(Vec<Entry>),
}

pub(crate) struct Entry {
    pub(crate) name: Ident,
    pub(crate) value: Value,
}

impl Entry {
    pub(crate) fn span(&self) -> Span {
        self.name.span()
    }

    pub(crate) fn read_literal(&self) -> syn::Result<&str> {
        match &self.value {
            Value::Literal(text) => Ok(text),
            _ => Err(syn::Error::new(
                self.span(),
                format!("'{}' wants a literal: {} = <VALUE>.", self.name, self.name),
            )),
        }
    }

    pub(crate) fn read_path(&self) -> syn::Result<&syn::Path> {
        match &self.value {
            Value::Path(path) => Ok(path),
            _ => Err(syn::Error::new(
                self.span(),
                format!(
                    "'{}' wants a path to a function: {} = <PATH>.",
                    self.name, self.name
                ),
            )),
        }
    }

    pub(crate) fn read_nested(&self) -> syn::Result<&[Entry]> {
        match &self.value {
            Value::Nested(entries) => Ok(entries),
            _ => Err(syn::Error::new(
                self.span(),
                format!(
                    "'{}' wants its options in parentheses: {}(...).",
                    self.name, self.name
                ),
            )),
        }
    }

    pub(crate) fn expect_flag(&self) -> syn::Result<()> {
        match &self.value {
            Value::Flag => Ok(()),
            _ => Err(syn::Error::new(
                self.span(),
                format!("'{}' takes no value.", self.name),
            )),
        }
    }

    pub(crate) fn read_paths(&self) -> syn::Result<Vec<syn::Path>> {
        match &self.value {
            Value::Paths(paths) => Ok(paths.clone()),
            Value::List(items) if items.is_empty() => Ok(Vec::new()),
            _ => Err(syn::Error::new(
                self.span(),
                format!(
                    "'{}' wants a list of types: {} = [<TYPE>, ...].",
                    self.name, self.name
                ),
            )),
        }
    }

    pub(crate) fn read_list(&self) -> syn::Result<&[String]> {
        match &self.value {
            Value::List(items) => Ok(items),
            _ => Err(syn::Error::new(
                self.span(),
                format!(
                    "'{}' wants a list of strings: {} = [\"<PATH>\"].",
                    self.name, self.name
                ),
            )),
        }
    }
}

impl Parse for Entry {
    fn parse(input: ParseStream) -> syn::Result<Self> {
        let name: Ident = input.parse()?;
        if input.peek(syn::token::Paren) {
            let inner;
            syn::parenthesized!(inner in input);
            let nested: Punctuated<Entry, Token![,]> = Punctuated::parse_terminated(&inner)?;
            return Ok(Self {
                name,
                value: Value::Nested(nested.into_iter().collect()),
            });
        }
        if !input.peek(Token![=]) {
            return Ok(Self {
                name,
                value: Value::Flag,
            });
        }
        input.parse::<Token![=]>()?;

        if input.peek(syn::token::Bracket) {
            let items;
            syn::bracketed!(items in input);
            if !items.is_empty() && !items.peek(LitStr) {
                let paths: Punctuated<syn::Path, Token![,]> = Punctuated::parse_terminated(&items)?;
                return Ok(Self {
                    name,
                    value: Value::Paths(paths.into_iter().collect()),
                });
            }
            let strings: Punctuated<LitStr, Token![,]> = Punctuated::parse_terminated(&items)?;
            return Ok(Self {
                name,
                value: Value::List(strings.iter().map(LitStr::value).collect()),
            });
        }

        if !input.peek(Lit) && !input.peek(Token![-]) {
            return Ok(Self {
                name,
                value: Value::Path(input.parse()?),
            });
        }

        let sign = if input.peek(Token![-]) {
            input.parse::<Token![-]>()?;
            "-"
        } else {
            ""
        };
        let literal: Lit = input.parse()?;
        Ok(Self {
            name,
            value: Value::Literal(format!("{sign}{}", read_literal_text(&literal)?)),
        })
    }
}

fn read_literal_text(literal: &Lit) -> syn::Result<String> {
    match literal {
        Lit::Str(value) => Ok(value.value()),
        Lit::Int(value) => Ok(value.base10_digits().to_string()),
        Lit::Float(value) => Ok(value.base10_digits().to_string()),
        Lit::Bool(value) => Ok(value.value().to_string()),
        other => Err(syn::Error::new(
            Span::call_site(),
            format!(
                "a value is a string, integer, float or bool literal, and this is a {}.",
                describe_literal_kind(other)
            ),
        )),
    }
}

fn describe_literal_kind(literal: &Lit) -> &'static str {
    match literal {
        Lit::Byte(_) | Lit::ByteStr(_) => "byte string",
        Lit::Char(_) => "char",
        Lit::CStr(_) => "C string",
        _ => "literal of another kind",
    }
}

pub(crate) fn parse_arguments(tokens: proc_macro2::TokenStream) -> syn::Result<Vec<Entry>> {
    let parsed = Punctuated::<Entry, Token![,]>::parse_terminated.parse2(tokens)?;
    Ok(parsed.into_iter().collect())
}

pub(crate) fn parse_entries(attributes: &[Attribute]) -> syn::Result<Vec<Entry>> {
    let mut found = Vec::new();
    for attribute in attributes
        .iter()
        .filter(|attribute| attribute.path().is_ident("ocel"))
    {
        let parsed = attribute.parse_args_with(Punctuated::<Entry, Token![,]>::parse_terminated)?;
        found.extend(parsed);
    }
    Ok(found)
}

#[cfg(test)]
mod tests {
    use super::{parse_arguments, parse_entries, Value};
    use syn::parse::Parser;
    use syn::Field;

    fn parsed(source: &str) -> Vec<(String, Value)> {
        let field = Field::parse_named
            .parse_str(source)
            .expect("a named struct field");
        parse_entries(&field.attrs)
            .expect("the attribute grammar")
            .into_iter()
            .map(|entry| (entry.name.to_string(), entry.value))
            .collect()
    }

    fn one(source: &str) -> Value {
        let mut all = parsed(source);
        assert_eq!(all.len(), 1, "want exactly one entry in {source}");
        all.remove(0).1
    }

    #[test]
    fn a_field_with_no_attribute_has_no_entries() {
        assert!(parsed("pub port: u16").is_empty());
    }

    #[test]
    fn a_bare_name_is_a_flag() {
        assert!(matches!(
            one("#[ocel(sensitive)] pub k: String"),
            Value::Flag
        ));
    }

    #[test]
    fn a_string_an_integer_a_float_and_a_bool_all_read_as_their_own_text() {
        for (source, want) in [
            (r#"#[ocel(default = "hello")] pub k: String"#, "hello"),
            ("#[ocel(default = 3000)] pub k: u16", "3000"),
            ("#[ocel(default = -1)] pub k: i32", "-1"),
            ("#[ocel(default = 1.5)] pub k: f64", "1.5"),
            ("#[ocel(default = true)] pub k: bool", "true"),
        ] {
            match one(source) {
                Value::Literal(text) => assert_eq!(text, want, "in {source}"),
                _ => panic!("{source} is not a literal"),
            }
        }
    }

    #[test]
    fn a_bracketed_value_is_a_list_of_strings() {
        match one(r#"#[ocel(folders = ["/apps/web", "/apps/api"])] pub k: bool"#) {
            Value::List(items) => assert_eq!(items, ["/apps/web", "/apps/api"]),
            _ => panic!("folders is not a list"),
        }
    }

    #[test]
    fn several_entries_and_several_attributes_all_come_back_in_order() {
        let names: Vec<String> =
            parsed(r#"#[ocel(key = "FLAG", sensitive)] #[ocel(folders = ["/a"])] pub k: String"#)
                .into_iter()
                .map(|(name, _)| name)
                .collect();
        assert_eq!(names, ["key", "sensitive", "folders"]);
    }

    #[test]
    fn a_bare_path_is_a_path_and_parentheses_hold_nested_entries() {
        let parsed = parse_arguments(quote::quote!(
            on_success = hooks::notify,
            retry(max_attempts = 5, min_delay = "1s"),
            ordered
        ))
        .expect("the attribute grammar");
        let names: Vec<String> = parsed.iter().map(|entry| entry.name.to_string()).collect();
        assert_eq!(names, ["on_success", "retry", "ordered"]);
        match &parsed[0].value {
            Value::Path(path) => assert_eq!(quote::quote!(#path).to_string(), "hooks :: notify"),
            _ => panic!("on_success is not a path"),
        }
        match &parsed[1].value {
            Value::Nested(nested) => {
                let names: Vec<String> =
                    nested.iter().map(|entry| entry.name.to_string()).collect();
                assert_eq!(names, ["max_attempts", "min_delay"]);
            }
            _ => panic!("retry is not nested"),
        }
    }

    #[test]
    fn a_value_of_a_kind_no_variable_takes_is_refused() {
        let field = Field::parse_named
            .parse_str("#[ocel(default = 'x')] pub k: char")
            .expect("a named struct field");
        let err = match parse_entries(&field.attrs) {
            Ok(_) => panic!("a char default was accepted"),
            Err(err) => err,
        };
        assert!(
            err.to_string().contains("char"),
            "error = {err}, want it to name the kind"
        );
    }
}
