use crate::Error;
use std::fmt::Display;
use std::str::FromStr;

const DELIVERED_PREFIX: &str = "OCEL_VAR_";
const APP_FOLDER_ENV: &str = "OCEL_APP_FOLDER";
const URL_KEY: &str = "OCEL_URL";

#[doc(hidden)]
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Class {
    Plain,
    Sensitive,
    Secret,
}

impl Class {
    pub(crate) fn name(self) -> &'static str {
        match self {
            Self::Plain => "plain",
            Self::Sensitive => "sensitive",
            Self::Secret => "secret",
        }
    }

    fn confidential(self) -> bool {
        self != Self::Plain
    }
}

/// A live variable: one whose value can be rotated underneath the running process. A field
/// of this type declares the variable under the secret class, and [`Secret::value`] resolves
/// the value on every call rather than once at load, so a rotation reaches the next call.
/// Printing a `Secret` prints a redaction, never the value.
#[derive(Clone, PartialEq, Eq)]
pub struct Secret {
    key: String,
}

impl Secret {
    /// The variable the secret was declared under.
    pub fn key(&self) -> &str {
        &self.key
    }

    /// The secret's current value, read from the environment on every call. It fails with
    /// [`Error::Unset`] when nothing stands for the key any more, the way the read of a
    /// missing variable fails, rather than hand back an empty secret.
    pub fn value(&self) -> Result<String, Error> {
        delivered(&self.key).ok_or_else(|| unset(&self.key))
    }
}

impl std::fmt::Display for Secret {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Secret({})", self.key)
    }
}

impl std::fmt::Debug for Secret {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        std::fmt::Display::fmt(self, f)
    }
}

/// The absolute url this app is served on, scheme and all: the deployed hostname, or the
/// local one under `ocel dev`. Ocel writes it; nothing declares it.
pub fn deployment_url() -> Result<String, Error> {
    delivered(URL_KEY).ok_or_else(|| Error::Undelivered {
        key: URL_KEY.to_string(),
    })
}

#[doc(hidden)]
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Boolean(bool);

impl From<Boolean> for bool {
    fn from(boolean: Boolean) -> Self {
        boolean.0
    }
}

impl FromStr for Boolean {
    type Err = NotABoolean;

    fn from_str(raw: &str) -> Result<Self, Self::Err> {
        match raw {
            "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(Self(true)),
            "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(Self(false)),
            _ => Err(NotABoolean),
        }
    }
}

#[doc(hidden)]
#[derive(Debug)]
pub struct NotABoolean;

impl Display for NotABoolean {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "want one of 1 t T TRUE true True 0 f F FALSE false False"
        )
    }
}

#[doc(hidden)]
pub fn value<T>(
    key: &str,
    class: Class,
    folders: &[&str],
    fallback: Option<&str>,
) -> Result<T, Error>
where
    T: FromStr,
    T::Err: Display,
{
    scoped(key, folders)?;
    let Some(raw) = delivered(key).or_else(|| fallback.map(str::to_string)) else {
        return Err(unset(key));
    };
    parse(key, class, &raw)
}

#[doc(hidden)]
pub fn optional<T>(
    key: &str,
    class: Class,
    folders: &[&str],
    fallback: Option<&str>,
) -> Result<Option<T>, Error>
where
    T: FromStr,
    T::Err: Display,
{
    scoped(key, folders)?;
    let Some(raw) = delivered(key).or_else(|| fallback.map(str::to_string)) else {
        return Ok(None);
    };
    parse(key, class, &raw).map(Some)
}

#[doc(hidden)]
pub fn secret(key: &str, folders: &[&str]) -> Result<Secret, Error> {
    scoped(key, folders)?;
    if delivered(key).is_none() {
        return Err(unset(key));
    }
    Ok(Secret {
        key: key.to_string(),
    })
}

#[doc(hidden)]
pub fn check<T>(raw: &str) -> Result<(), String>
where
    T: FromStr,
    T::Err: Display,
{
    raw.parse::<T>().map(|_| ()).map_err(|err| err.to_string())
}

pub(crate) fn complaint(class: Class, message: &str) -> String {
    if !class.confidential() {
        return message.to_string();
    }
    format!(
        "withheld, because a '{}' value's parse message can quote the value itself",
        class.name()
    )
}

fn parse<T>(key: &str, class: Class, raw: &str) -> Result<T, Error>
where
    T: FromStr,
    T::Err: Display,
{
    raw.parse().map_err(|err: T::Err| Error::Invalid {
        key: key.to_string(),
        detail: complaint(class, &err.to_string()),
    })
}

fn scoped(key: &str, folders: &[&str]) -> Result<(), Error> {
    if folders.is_empty() {
        return Ok(());
    }
    let binding = std::env::var(APP_FOLDER_ENV).unwrap_or_default();
    if folders.contains(&binding.as_str()) {
        return Ok(());
    }
    Err(Error::Scope {
        key: key.to_string(),
        folders: folders.iter().map(|folder| folder.to_string()).collect(),
        binding,
    })
}

fn delivered(key: &str) -> Option<String> {
    std::env::var(format!("{DELIVERED_PREFIX}{key}"))
        .or_else(|_| std::env::var(key))
        .ok()
}

fn unset(key: &str) -> Error {
    Error::Unset {
        key: key.to_string(),
    }
}
