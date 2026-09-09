use proc_macro2::Span;
use syn::parse::{Parse, ParseStream};
use syn::punctuated::Punctuated;
use syn::{Attribute, Ident, Lit, LitStr, Token};

pub(crate) enum Value {
    Flag,
    Literal(String),
    List(Vec<String>),
}

pub(crate) struct Entry {
    pub(crate) name: Ident,
    pub(crate) value: Value,
}

impl Entry {
    pub(crate) fn span(&self) -> Span {
        self.name.span()
    }

    pub(crate) fn literal(&self) -> syn::Result<&str> {
        match &self.value {
            Value::Literal(text) => Ok(text),
            _ => Err(syn::Error::new(
                self.span(),
                format!("'{}' wants a literal: {} = <VALUE>.", self.name, self.name),
            )),
        }
    }

    pub(crate) fn list(&self) -> syn::Result<&[String]> {
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
            let strings: Punctuated<LitStr, Token![,]> = Punctuated::parse_terminated(&items)?;
            return Ok(Self {
                name,
                value: Value::List(strings.iter().map(LitStr::value).collect()),
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
            value: Value::Literal(format!("{sign}{}", text(&literal)?)),
        })
    }
}

fn text(literal: &Lit) -> syn::Result<String> {
    match literal {
        Lit::Str(value) => Ok(value.value()),
        Lit::Int(value) => Ok(value.base10_digits().to_string()),
        Lit::Float(value) => Ok(value.base10_digits().to_string()),
        Lit::Bool(value) => Ok(value.value().to_string()),
        other => Err(syn::Error::new(
            Span::call_site(),
            format!(
                "a value is a string, integer, float or bool literal, and this is a {}.",
                kind(other)
            ),
        )),
    }
}

fn kind(literal: &Lit) -> &'static str {
    match literal {
        Lit::Byte(_) | Lit::ByteStr(_) => "byte string",
        Lit::Char(_) => "char",
        Lit::CStr(_) => "C string",
        _ => "literal of another kind",
    }
}

pub(crate) fn entries(attrs: &[Attribute]) -> syn::Result<Vec<Entry>> {
    let mut found = Vec::new();
    for attr in attrs.iter().filter(|attr| attr.path().is_ident("ocel")) {
        let parsed = attr.parse_args_with(Punctuated::<Entry, Token![,]>::parse_terminated)?;
        found.extend(parsed);
    }
    Ok(found)
}

#[cfg(test)]
mod tests {
    use super::{entries, Value};
    use syn::Field;

    fn parsed(source: &str) -> Vec<(String, Value)> {
        let field = Field::parse_named
            .parse_str(source)
            .expect("a named struct field");
        entries(&field.attrs)
            .expect("the attribute grammar")
            .into_iter()
            .map(|entry| (entry.name.to_string(), entry.value))
            .collect()
    }

    use syn::parse::Parser;

    fn one(source: &str) -> Value {
        let mut all = parsed(source);
        assert_eq!(all.len(), 1, "want exactly one entry in {source}");
        all.remove(0).1
    }

    #[test]
    fn a_field_with_no_attribute_carries_no_entries() {
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
    fn a_value_of_a_kind_no_variable_takes_is_refused() {
        let field = Field::parse_named
            .parse_str("#[ocel(default = 'x')] pub k: char")
            .expect("a named struct field");
        let err = match entries(&field.attrs) {
            Ok(_) => panic!("a char default was accepted"),
            Err(err) => err,
        };
        assert!(
            err.to_string().contains("char"),
            "error = {err}, want it to name the kind"
        );
    }
}
