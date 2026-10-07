use super::{Counter, Json, Kv, KvEntryDeclaration, KvKey, List, Set, Shape, Text};
use crate::binding::kv_binding_key;
use crate::declare::is_discovering;
use crate::proto::common::bindings::v1::KvProperties;
use crate::Error;
use redis::aio::MultiplexedConnection;
use redis::{Cmd, FromRedisValue, Pipeline};
use serde::de::DeserializeOwned;
use serde::Serialize;
use std::collections::HashSet;
use std::future::{Future, IntoFuture};
use std::marker::PhantomData;
use std::pin::Pin;
use std::time::Duration;

type ResultFuture<'a, T> = Pin<Box<dyn Future<Output = Result<T, Error>> + Send + 'a>>;

fn verified_host(properties: &KvProperties) -> &str {
    if properties.tls && !properties.tls_server_name.is_empty() {
        &properties.tls_server_name
    } else {
        &properties.host
    }
}

struct Forward {
    host: String,
    port: u16,
}

impl redis::io::AsyncDNSResolver for Forward {
    fn resolve<'a, 'b: 'a>(
        &'a self,
        _host: &'b str,
        _port: u16,
    ) -> redis::RedisFuture<'a, Box<dyn Iterator<Item = std::net::SocketAddr> + Send + 'a>> {
        Box::pin(async move {
            let addresses = tokio::net::lookup_host((self.host.as_str(), self.port)).await?;
            Ok(Box::new(addresses.collect::<Vec<_>>().into_iter())
                as Box<dyn Iterator<Item = std::net::SocketAddr> + Send>)
        })
    }
}

impl Kv {
    /// The redis-rs client for the store over the delivered binding, encrypted when the
    /// binding requires TLS, and trusting only the binding's certificate authority when it
    /// delivers one. A binding that names a TLS server name, as one pointing at a port
    /// forward does, gives a client addressed by that name, so its certificate is verified
    /// against it; [`Kv::connection`] reaches such a store through the forward, while a
    /// connection opened from this client resolves the name itself. It is built on the first call and the same client is returned on every
    /// one after. It fails when no binding was delivered for the name, when the delivered
    /// authority holds no PEM certificate, and during discovery.
    pub fn client(&self) -> Result<redis::Client, Error> {
        self.open_client("client")
    }

    fn open_client(&self, access: &str) -> Result<redis::Client, Error> {
        let properties = self.read_properties(access)?;
        if let Some(client) = self.client.get() {
            return Ok(client.clone());
        }
        let port = self.read_port(&properties)?;
        let address = if properties.tls {
            redis::ConnectionAddr::TcpTls {
                host: verified_host(&properties).to_string(),
                port,
                insecure: false,
                tls_params: None,
            }
        } else {
            redis::ConnectionAddr::Tcp(properties.host.clone(), port)
        };
        let mut settings = redis::RedisConnectionInfo::default();
        if !properties.username.is_empty() {
            settings = settings.set_username(properties.username.clone());
        }
        if !properties.password.is_empty() {
            settings = settings.set_password(properties.password.clone());
        }
        let info =
            redis::IntoConnectionInfo::into_connection_info(address)?.set_redis_settings(settings);
        let client = if properties.tls && !properties.ca_pem.is_empty() {
            redis::Client::build_with_tls(
                info,
                redis::TlsCertificates {
                    client_tls: None,
                    root_cert: Some(self.read_authority(&properties)?),
                },
            )?
        } else {
            redis::Client::open(info)?
        };
        Ok(self.client.get_or_init(|| client).clone())
    }

    fn read_authority(&self, properties: &KvProperties) -> Result<Vec<u8>, Error> {
        use rustls::pki_types::{pem::PemObject, CertificateDer};
        let pem = properties.ca_pem.as_bytes();
        let mut certificates = CertificateDer::pem_slice_iter(pem);
        match certificates.next() {
            Some(Ok(_)) if certificates.all(|parsed| parsed.is_ok()) => Ok(pem.to_vec()),
            _ => Err(Error::InvalidKvAuthority {
                key: kv_binding_key(&self.name),
            }),
        }
    }

    /// The multiplexed connection every operation on the store shares, opened on the first
    /// call and cloned on every one after. It fails as [`Kv::client`] does, and when the
    /// store cannot be reached.
    pub async fn connection(&self) -> Result<MultiplexedConnection, Error> {
        self.open_connection("connection").await
    }

    async fn open_connection(&self, access: &str) -> Result<MultiplexedConnection, Error> {
        if is_discovering() {
            return Err(self.refuse_unprovisioned(access));
        }
        let connection = self
            .connection
            .get_or_try_init(|| async {
                let client = self.open_client(access)?;
                let properties = self.read_properties(access)?;
                let mut config = redis::AsyncConnectionConfig::new();
                if verified_host(&properties) != properties.host {
                    config = config.set_dns_resolver(Forward {
                        host: properties.host.clone(),
                        port: self.read_port(&properties)?,
                    });
                }
                Ok::<_, Error>(
                    client
                        .get_multiplexed_async_connection_with_config(&config)
                        .await?,
                )
            })
            .await?;
        Ok(connection.clone())
    }

    /// The entry of this store whose key is `key`: the handle its value is read and
    /// written through, with the operations its shape allows. Every operation on it fails
    /// without reaching the store when the store's `entries` do not list `K`.
    pub fn entry<K: KvKey>(&self, key: K) -> Entry<'_, K::Shape> {
        Entry {
            kv: self,
            key: key.build_key(),
            declared: K::ENTRY,
            unlisted: (!self.lists::<K>()).then(std::any::type_name::<K>),
            shape: PhantomData,
        }
    }

    fn lists<K: KvKey>(&self) -> bool {
        self.entries.contains(&std::any::TypeId::of::<K>())
    }

    fn refuse_unlisted(&self, key: &str) -> Error {
        Error::UndeclaredKvEntry {
            store: self.name().to_string(),
            key: key.to_string(),
        }
    }

    /// The value under each key, in order, read as pipelined single-key commands in one
    /// round trip. A key that holds nothing reads as `None`. It fails without reaching the
    /// store when the store's `entries` do not list `K`.
    pub async fn get_many<K>(
        &self,
        keys: &[K],
    ) -> Result<Vec<Option<<K::Shape as Readable>::Value>>, Error>
    where
        K: KvKey,
        K::Shape: Readable,
    {
        if !self.lists::<K>() {
            return Err(self.refuse_unlisted(std::any::type_name::<K>()));
        }
        let built: Vec<String> = keys.iter().map(KvKey::build_key).collect();
        let mut connection = self.open_connection("get_many").await?;
        let mut pipeline = redis::pipe();
        for key in &built {
            pipeline.get(key);
        }
        let values: Vec<Option<String>> = pipeline.query_async(&mut connection).await?;
        built
            .iter()
            .zip(values)
            .map(|(key, raw)| K::Shape::decode(key, raw, &K::ENTRY))
            .collect()
    }
}

/// A shape whose value is read whole: [`Text`], [`Counter`] and [`Json<V>`](Json). It
/// bounds [`Entry::get`] and [`Kv::get_many`], so code generic over a [`KvKey`] that reads
/// its value names `K::Shape: Readable`. Like [`Shape`], it is sealed: the shapes implement
/// it, and an app does not.
pub trait Readable: Shape {
    /// The value a read answers.
    type Value;

    #[doc(hidden)]
    fn decode(
        key: &str,
        raw: Option<String>,
        declared: &KvEntryDeclaration,
    ) -> Result<Option<Self::Value>, Error>;
}

impl Readable for Text {
    type Value = String;

    fn decode(
        _: &str,
        raw: Option<String>,
        _: &KvEntryDeclaration,
    ) -> Result<Option<String>, Error> {
        Ok(raw)
    }
}

impl Readable for Counter {
    type Value = i64;

    fn decode(
        key: &str,
        raw: Option<String>,
        _: &KvEntryDeclaration,
    ) -> Result<Option<i64>, Error> {
        raw.map(|text| {
            text.parse().map_err(|_| Error::InvalidKvValue {
                key: key.to_string(),
                reason: format!("holds \"{text}\", which is no integer"),
            })
        })
        .transpose()
    }
}

impl<V: DeserializeOwned> Readable for Json<V> {
    type Value = V;

    fn decode(
        key: &str,
        raw: Option<String>,
        declared: &KvEntryDeclaration,
    ) -> Result<Option<V>, Error> {
        let Some(raw) = raw else { return Ok(None) };
        match serde_json::from_str(&raw) {
            Ok(value) => Ok(Some(value)),
            Err(_) if declared.miss_on_invalid => Ok(None),
            Err(err) => Err(Error::InvalidKvValue {
                key: key.to_string(),
                reason: format!(
                    "holds a value that does not decode into a {}: {err}",
                    std::any::type_name::<V>()
                ),
            }),
        }
    }
}

/// One key of a [`Kv`] store's entry, with the operations its shape `S` allows. Take it
/// with [`Kv::entry`].
pub struct Entry<'a, S> {
    kv: &'a Kv,
    key: String,
    declared: KvEntryDeclaration,
    unlisted: Option<&'static str>,
    shape: PhantomData<fn() -> S>,
}

impl<'a, S> Entry<'a, S> {
    /// The key as it is written in the store, its parameters percent-encoded.
    pub fn key(&self) -> &str {
        &self.key
    }

    fn access(&self, operation: &str) -> String {
        format!("{}.{operation}", self.declared.name)
    }

    fn refuse_unlisted(&self) -> Result<(), Error> {
        match self.unlisted {
            Some(key) => Err(self.kv.refuse_unlisted(key)),
            None => Ok(()),
        }
    }

    async fn query<T: FromRedisValue>(&self, operation: &str, command: Cmd) -> Result<T, Error> {
        self.refuse_unlisted()?;
        let mut connection = self.kv.open_connection(&self.access(operation)).await?;
        Ok(command.query_async(&mut connection).await?)
    }

    /// Deletes the key, answering whether it held a value.
    pub async fn delete(&self) -> Result<bool, Error> {
        let deleted: i64 = self
            .query("delete", redis::cmd("DEL").arg(&self.key).to_owned())
            .await?;
        Ok(deleted > 0)
    }

    fn write<T>(&self, operation: &'static str, command: Cmd, ttl: TtlPlacement) -> Write<'a, T> {
        Write {
            kv: self.kv,
            access: self.access(operation),
            key: self.key.clone(),
            command,
            placement: ttl,
            ttl: self.declared.ttl.map_or(Ttl::Clear, Ttl::Expire),
            refused: self.refuse_unlisted().err(),
            output: PhantomData,
        }
    }
}

impl<S: Readable> Entry<'_, S> {
    /// The value under the key, or `None` when there is none.
    pub async fn get(&self) -> Result<Option<S::Value>, Error> {
        let raw: Option<String> = self
            .query("get", redis::cmd("GET").arg(&self.key).to_owned())
            .await?;
        S::decode(&self.key, raw, &self.declared)
    }
}

impl<'a> Entry<'a, Text> {
    /// Writes `value` under the key, with the entry's TTL unless the write says otherwise.
    pub fn set(&self, value: impl Into<String>) -> Write<'a, ()> {
        let command = redis::cmd("SET")
            .arg(&self.key)
            .arg(value.into())
            .to_owned();
        self.write("set", command, TtlPlacement::SetOptions)
    }
}

impl<'a> Entry<'a, Counter> {
    /// Writes `value` under the key, with the entry's TTL unless the write says otherwise.
    pub fn set(&self, value: i64) -> Write<'a, ()> {
        let command = redis::cmd("SET").arg(&self.key).arg(value).to_owned();
        self.write("set", command, TtlPlacement::SetOptions)
    }

    /// Adds `by` to the integer under the key, from 0 when there is none, and answers the
    /// sum. The entry's TTL is applied in the same transaction.
    pub fn increment(&self, by: i64) -> Write<'a, i64> {
        let command = redis::cmd("INCRBY").arg(&self.key).arg(by).to_owned();
        self.write("increment", command, TtlPlacement::Transaction)
    }

    /// Subtracts `by` from the integer under the key, as [`Entry::increment`] adds.
    pub fn decrement(&self, by: i64) -> Write<'a, i64> {
        let command = redis::cmd("DECRBY").arg(&self.key).arg(by).to_owned();
        self.write("decrement", command, TtlPlacement::Transaction)
    }
}

impl<'a, V: Serialize> Entry<'a, Json<V>> {
    /// Writes `value` under the key as its JSON, with the entry's TTL unless the write says
    /// otherwise.
    pub fn set(&self, value: &V) -> Write<'a, ()> {
        let mut written = self.write("set", Cmd::new(), TtlPlacement::SetOptions);
        match serde_json::to_string(value) {
            Ok(encoded) => {
                written.command = redis::cmd("SET").arg(&self.key).arg(encoded).to_owned()
            }
            Err(err) => {
                written.refused.get_or_insert(Error::InvalidKvValue {
                    key: self.key.clone(),
                    reason: format!("would hold a value that does not encode as JSON: {err}"),
                });
            }
        }
        written
    }
}

fn strings<I, T>(values: I) -> Vec<String>
where
    I: IntoIterator<Item = T>,
    T: Into<String>,
{
    values.into_iter().map(Into::into).collect()
}

impl<'a, S> Entry<'a, S> {
    fn refuse_no_values(&self, operation: &str, values: &[String]) -> Result<(), Error> {
        if values.is_empty() {
            return Err(Error::EmptyKvWrite {
                access: self.access(operation),
            });
        }
        Ok(())
    }

    fn write_values(
        &self,
        operation: &'static str,
        command: &str,
        values: Vec<String>,
    ) -> Write<'a, i64> {
        let mut written = self.write(
            operation,
            redis::cmd(command).arg(&self.key).arg(&values).to_owned(),
            TtlPlacement::Transaction,
        );
        if let Err(err) = self.refuse_no_values(operation, &values) {
            written.refused.get_or_insert(err);
        }
        written
    }
}

impl<'a> Entry<'a, List> {
    /// Appends `values` to the end of the list and answers its new length, applying the
    /// entry's TTL in the same transaction. It fails without reaching the store when
    /// `values` is empty.
    pub fn push_back<I, T>(&self, values: I) -> Write<'a, i64>
    where
        I: IntoIterator<Item = T>,
        T: Into<String>,
    {
        self.write_values("push_back", "RPUSH", strings(values))
    }

    /// Prepends `values`, in the order given, to the start of the list and answers its new
    /// length, as [`Entry::push_back`] does.
    pub fn push_front<I, T>(&self, values: I) -> Write<'a, i64>
    where
        I: IntoIterator<Item = T>,
        T: Into<String>,
    {
        let mut values = strings(values);
        values.reverse();
        self.write_values("push_front", "LPUSH", values)
    }

    /// Removes and answers the last value, or `None` when the list is empty.
    pub async fn pop_back(&self) -> Result<Option<String>, Error> {
        self.query("pop_back", redis::cmd("RPOP").arg(&self.key).to_owned())
            .await
    }

    /// Removes and answers the first value, or `None` when the list is empty.
    pub async fn pop_front(&self) -> Result<Option<String>, Error> {
        self.query("pop_front", redis::cmd("LPOP").arg(&self.key).to_owned())
            .await
    }

    /// The value at `index`, counting back from the end when negative, or `None` past
    /// either end.
    pub async fn get(&self, index: isize) -> Result<Option<String>, Error> {
        self.query(
            "get",
            redis::cmd("LINDEX").arg(&self.key).arg(index).to_owned(),
        )
        .await
    }

    /// The values from `start` to `stop`, both included and counted back from the end when
    /// negative, so `range(0, -1)` is the whole list.
    pub async fn range(&self, start: isize, stop: isize) -> Result<Vec<String>, Error> {
        let command = redis::cmd("LRANGE")
            .arg(&self.key)
            .arg(start)
            .arg(stop)
            .to_owned();
        self.query("range", command).await
    }

    /// How many values the list holds.
    pub async fn len(&self) -> Result<usize, Error> {
        self.query("len", redis::cmd("LLEN").arg(&self.key).to_owned())
            .await
    }

    /// Whether the list holds no values.
    pub async fn is_empty(&self) -> Result<bool, Error> {
        Ok(self.len().await? == 0)
    }
}

impl<'a> Entry<'a, Set> {
    /// Adds `members` to the set and answers how many were not in it already, applying the
    /// entry's TTL in the same transaction. It fails without reaching the store when
    /// `members` is empty.
    pub fn insert<I, T>(&self, members: I) -> Write<'a, i64>
    where
        I: IntoIterator<Item = T>,
        T: Into<String>,
    {
        self.write_values("insert", "SADD", strings(members))
    }

    /// Removes `members` from the set and answers how many were in it. It fails without
    /// reaching the store when `members` is empty.
    pub async fn remove<I, T>(&self, members: I) -> Result<i64, Error>
    where
        I: IntoIterator<Item = T>,
        T: Into<String>,
    {
        let members = strings(members);
        self.refuse_no_values("remove", &members)?;
        let command = redis::cmd("SREM").arg(&self.key).arg(members).to_owned();
        self.query("remove", command).await
    }

    /// Whether `member` is in the set.
    pub async fn contains(&self, member: &str) -> Result<bool, Error> {
        let command = redis::cmd("SISMEMBER")
            .arg(&self.key)
            .arg(member)
            .to_owned();
        self.query("contains", command).await
    }

    /// How many members the set holds.
    pub async fn len(&self) -> Result<usize, Error> {
        self.query("len", redis::cmd("SCARD").arg(&self.key).to_owned())
            .await
    }

    /// Whether the set holds no members.
    pub async fn is_empty(&self) -> Result<bool, Error> {
        Ok(self.len().await? == 0)
    }

    /// Every member of the set.
    pub async fn members(&self) -> Result<HashSet<String>, Error> {
        self.query("members", redis::cmd("SMEMBERS").arg(&self.key).to_owned())
            .await
    }
}

#[derive(Clone, Copy)]
enum Ttl {
    Expire(Duration),
    Keep,
    Clear,
}

#[derive(Clone, Copy)]
enum TtlPlacement {
    SetOptions,
    Transaction,
}

/// A write to an entry, sent when it is awaited. The entry's TTL is applied with it
/// unless [`Write::ttl`], [`Write::keep_ttl`] or [`Write::clear_ttl`] says otherwise.
#[must_use = "a write does nothing until it is awaited"]
pub struct Write<'a, T> {
    kv: &'a Kv,
    access: String,
    key: String,
    command: Cmd,
    placement: TtlPlacement,
    ttl: Ttl,
    refused: Option<Error>,
    output: PhantomData<fn() -> T>,
}

impl<T> Write<'_, T> {
    /// Leaves the key to live for `after` instead of the entry's TTL.
    pub fn ttl(mut self, after: Duration) -> Self {
        self.ttl = Ttl::Expire(after);
        self
    }

    /// Keeps the key's current TTL, so the write neither extends nor clears it.
    pub fn keep_ttl(mut self) -> Self {
        self.ttl = Ttl::Keep;
        self
    }

    /// Clears the key's TTL, so the key lives until it is deleted or evicted.
    pub fn clear_ttl(mut self) -> Self {
        self.ttl = Ttl::Clear;
        self
    }
}

fn milliseconds(after: Duration) -> Result<u64, Error> {
    match u64::try_from(after.as_millis()) {
        Ok(milliseconds) if milliseconds >= 1 => Ok(milliseconds),
        _ => Err(Error::InvalidKvTtl { ttl: after }),
    }
}

impl<'a, T: FromRedisValue + Send + 'a> IntoFuture for Write<'a, T> {
    type Output = Result<T, Error>;
    type IntoFuture = ResultFuture<'a, T>;

    fn into_future(self) -> Self::IntoFuture {
        Box::pin(async move {
            if let Some(refused) = self.refused {
                return Err(refused);
            }
            let mut command = self.command;
            let mut connection = self.kv.open_connection(&self.access).await?;
            match self.placement {
                TtlPlacement::SetOptions => {
                    match self.ttl {
                        Ttl::Expire(after) => command.arg("PX").arg(milliseconds(after)?),
                        Ttl::Keep => command.arg("KEEPTTL"),
                        Ttl::Clear => &mut command,
                    };
                    Ok(command.query_async(&mut connection).await?)
                }
                TtlPlacement::Transaction => {
                    let mut pipeline = Pipeline::new();
                    pipeline.atomic().add_command(command);
                    match self.ttl {
                        Ttl::Expire(after) => {
                            pipeline
                                .cmd("PEXPIRE")
                                .arg(&self.key)
                                .arg(milliseconds(after)?)
                                .ignore();
                        }
                        Ttl::Clear => {
                            pipeline.cmd("PERSIST").arg(&self.key).ignore();
                        }
                        Ttl::Keep => {}
                    }
                    let (result,): (T,) = pipeline.query_async(&mut connection).await?;
                    Ok(result)
                }
            }
        })
    }
}
