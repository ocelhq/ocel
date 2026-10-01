//! The shapes a [`KvKey`] declares its entry as, and the handles an entry
//! of a [`Kv`] store is read and written through.

#[cfg(feature = "kv")]
mod entry;

use crate::binding::{percent_encode, read_kv};
use crate::declare::is_discovering;
use crate::proto::app::resources::v1::KvShape;
use crate::proto::common::bindings::v1::KvProperties;
use crate::Error;
use std::marker::PhantomData;
use std::time::Duration;

#[cfg(feature = "kv")]
pub use entry::{Entry, Readable, Write};

/// A key-value store an app declares, one Valkey instance of its own. A field of this type
/// in a struct deriving [`Resources`](macro@crate::Resources) is the declaration, and its
/// `entries` are the [`KvKey`] types whose keys it holds:
///
/// ```ignore
/// #[derive(ocel::KvKey)]
/// #[ocel(pattern = "requests/:user_id", counter, ttl = "10s")]
/// pub struct Requests {
///     pub user_id: String,
/// }
///
/// #[derive(ocel::Resources)]
/// pub struct Infra {
///     #[ocel(eviction = "allkeys-lru", memory = "256mb", entries = [Requests])]
///     pub cache: ocel::Kv,
/// }
///
/// let count = infra.cache.entry(Requests { user_id }).increment(1).await?;
/// ```
#[derive(Clone)]
pub struct Kv {
    name: String,
    #[cfg(feature = "kv")]
    client: std::sync::Arc<std::sync::OnceLock<redis::Client>>,
    #[cfg(feature = "kv")]
    connection: std::sync::Arc<tokio::sync::OnceCell<redis::aio::MultiplexedConnection>>,
    #[cfg(feature = "kv")]
    entries: Vec<std::any::TypeId>,
}

impl Kv {
    /// Take the handle for the store named `name`. Prefer
    /// [`Resources`](macro@crate::Resources), which declares the store as well as handing
    /// back its handle.
    pub fn new(name: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            #[cfg(feature = "kv")]
            client: std::sync::Arc::default(),
            #[cfg(feature = "kv")]
            connection: std::sync::Arc::default(),
            #[cfg(feature = "kv")]
            entries: Vec::new(),
        }
    }

    /// The handle with `K`'s entry listed among the entries it reaches.
    /// [`Resources`](macro@crate::Resources) lists each of a store's `entries` this way, and
    /// [`Kv::entry`] and [`Kv::get_many`] refuse a key whose type the handle does not list,
    /// since the store never declared its pattern.
    pub fn with_entry<K: KvKey>(self) -> Self {
        Self {
            #[cfg(feature = "kv")]
            entries: self
                .entries
                .iter()
                .copied()
                .chain([std::any::TypeId::of::<K>()])
                .collect(),
            ..self
        }
    }

    /// The name the store was declared under, and the name its binding is delivered as.
    pub fn name(&self) -> &str {
        &self.name
    }

    /// The store's URL, `redis://` or `rediss://` when it requires TLS, with the delivered
    /// credentials percent-encoded, for tools that take one. It fails when no binding was
    /// delivered for the name, and during discovery.
    pub fn connection_string(&self) -> Result<String, Error> {
        let properties = self.read_properties("connection_string")?;
        Ok(format!(
            "{}://{}:{}@{}:{}",
            if properties.tls { "rediss" } else { "redis" },
            percent_encode(&properties.username),
            percent_encode(&properties.password),
            properties.host,
            self.read_port(&properties)?,
        ))
    }

    fn read_properties(&self, access: &str) -> Result<KvProperties, Error> {
        if is_discovering() {
            return Err(self.refuse_unprovisioned(access));
        }
        read_kv(&self.name)
    }

    fn read_port(&self, properties: &KvProperties) -> Result<u16, Error> {
        match u16::try_from(properties.port) {
            Ok(port) if port > 0 => Ok(port),
            _ => Err(Error::InvalidKvPort {
                key: format!("OCEL_RESOURCE_KV_{}", self.name),
                port: properties.port,
            }),
        }
    }

    fn refuse_unprovisioned(&self, access: &str) -> Error {
        Error::Unprovisioned {
            resource: format!("kv(\"{}\")", self.name),
            access: access.to_string(),
        }
    }
}

/// The key of one entry of a [`Kv`] store: a struct whose fields are the
/// parameters of the entry's pattern. Derive it with [`macro@crate::KvKey`], which checks the
/// fields against the pattern when the app builds.
pub trait KvKey: 'static {
    /// The entry's shape: [`Text`], [`Counter`], [`Json<V>`](Json), [`List`] or [`Set`].
    type Shape: Shape;

    #[doc(hidden)]
    const ENTRY: KvEntryDeclaration;

    #[doc(hidden)]
    fn build_key(&self) -> String;
}

#[doc(hidden)]
#[derive(Clone, Copy, Debug)]
pub struct KvEntryDeclaration {
    pub name: &'static str,
    pub pattern: &'static str,
    pub shape: ShapeKind,
    pub ttl: Option<Duration>,
    pub miss_on_invalid: bool,
    pub file: &'static str,
    pub line: u32,
}

#[doc(hidden)]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ShapeKind {
    Text,
    Counter,
    Json,
    List,
    Set,
}

impl ShapeKind {
    pub(crate) fn to_wire(self) -> KvShape {
        match self {
            Self::Text => KvShape::KV_SHAPE_TEXT,
            Self::Counter => KvShape::KV_SHAPE_COUNTER,
            Self::Json => KvShape::KV_SHAPE_JSON,
            Self::List => KvShape::KV_SHAPE_LIST,
            Self::Set => KvShape::KV_SHAPE_SET,
        }
    }
}

/// What an entry holds under each key. Its implementors are the five shapes a
/// [`KvKey`] declares.
pub trait Shape: sealed::Sealed {}

mod sealed {
    pub trait Sealed {}
}

/// A `text` entry: a string under each key.
pub struct Text;

/// A `counter` entry: an integer under each key, changed atomically.
pub struct Counter;

/// A `json` entry: a `V` under each key, stored as its serde JSON.
pub struct Json<V>(PhantomData<fn() -> V>);

/// A `list` entry: a list of strings under each key.
pub struct List;

/// A `set` entry: a set of strings under each key.
pub struct Set;

impl sealed::Sealed for Text {}
impl sealed::Sealed for Counter {}
impl<V> sealed::Sealed for Json<V> {}
impl sealed::Sealed for List {}
impl sealed::Sealed for Set {}
impl Shape for Text {}
impl Shape for Counter {}
impl<V> Shape for Json<V> {}
impl Shape for List {}
impl Shape for Set {}

/// A value a [`KvKey`] field can hold: a string or an integer, written into
/// the key percent-encoded so a `/` in it cannot name another entry's key.
pub trait KvParameter {
    #[doc(hidden)]
    fn write_parameter(&self, key: &mut String);
}

impl KvParameter for str {
    fn write_parameter(&self, key: &mut String) {
        key.push_str(&percent_encode(self));
    }
}

impl KvParameter for String {
    fn write_parameter(&self, key: &mut String) {
        self.as_str().write_parameter(key);
    }
}

impl<T: KvParameter + ?Sized> KvParameter for &T {
    fn write_parameter(&self, key: &mut String) {
        (**self).write_parameter(key);
    }
}

macro_rules! integer_parameters {
    ($($integer:ty),*) => {
        $(impl KvParameter for $integer {
            fn write_parameter(&self, key: &mut String) {
                key.push_str(&self.to_string());
            }
        })*
    };
}

integer_parameters!(i8, i16, i32, i64, i128, isize, u8, u16, u32, u64, u128, usize);

#[doc(hidden)]
pub fn write_parameter<T: KvParameter + ?Sized>(key: &mut String, value: &T) {
    value.write_parameter(key);
}

pub(crate) struct ParsedPattern<'a> {
    pub(crate) segments: Vec<Option<&'a str>>,
}

impl<'a> ParsedPattern<'a> {
    pub(crate) fn parse(pattern: &'a str) -> Self {
        Self {
            segments: pattern
                .split('/')
                .map(|segment| (!segment.starts_with(':')).then_some(segment))
                .collect(),
        }
    }

    pub(crate) fn overlaps(&self, other: &ParsedPattern) -> bool {
        self.segments.len() == other.segments.len()
            && self
                .segments
                .iter()
                .zip(&other.segments)
                .all(|(mine, theirs)| match (mine, theirs) {
                    (Some(mine), Some(theirs)) => mine == theirs,
                    _ => true,
                })
    }
}
