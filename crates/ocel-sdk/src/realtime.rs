//! Realtime channels: typed pub/sub that browsers subscribe to through the app's realtime
//! handler and the server publishes on. It needs the `realtime` feature.
//!
//! A channel is a struct deriving [`Channel`](macro@crate::Channel), whose fields are the
//! params of its pattern; it registers at link time, so discovery reads it without loading
//! anything. Rules are attached at runtime:
//!
//! ```ignore
//! #[derive(ocel::Channel)]
//! #[ocel(realtime = "app", pattern = "orders/:order_id", event = OrderEvent)]
//! pub struct Orders {
//!     pub order_id: String,
//! }
//!
//! let rt = ocel::realtime::Realtime::builder("app")
//!     .authorize(|request| async move { Ok::<_, ocel::Error>(session_of(&request)) })
//!     .subscribe::<Orders>(|ctx| async move { Ok::<_, ocel::Error>(owns(&ctx.auth, &ctx.params.order_id)) })
//!     .build()?;
//!
//! rt.publish(&Orders { order_id }, &OrderEvent::Shipped).await?;
//! let app = axum::Router::new().nest_service("/api/realtime", ocel::realtime::axum::router(rt));
//! ```

#[cfg(feature = "axum")]
pub mod axum;
mod denial;
mod handler;
mod token;
mod transport;
mod wire;

pub use denial::DenialCode;

use crate::binding::read_realtime;
use crate::declare::{is_discovering, Schema};
use crate::proto::app::realtime::v1::{PublishRequest, RealtimeServiceClient};
use crate::proto::common::bindings::v1::RealtimeProperties;
use crate::run::BoxFuture;
use crate::runtime::read_client_config;
use crate::Error;
use bytes::Bytes;
use connectrpc::client::HttpClient;
use futures_util::FutureExt;
use serde::de::DeserializeOwned;
use serde::Serialize;
use std::any::Any;
use std::collections::{BTreeMap, HashMap};
use std::fmt::Display;
use std::future::Future;
use std::marker::PhantomData;
use std::panic::AssertUnwindSafe;
use std::sync::{Arc, OnceLock};
use std::time::Duration;
use wire::{encode_wire_channel, is_channel_segment, ChannelPattern, SEGMENT_RULE};

const DEFAULT_TOKEN_TTL: Duration = Duration::from_secs(60);
const MAX_EVENT_BYTES: usize = 240 * 1024;

/// A channel pattern of a realtime resource, implemented by
/// [`#[derive(ocel::Channel)]`](macro@crate::Channel) on the struct of its params.
pub trait Channel: Sized + Send + Sync + 'static {
    /// The event every channel of the pattern carries, encoded as JSON.
    type Event: Serialize + DeserializeOwned + Send + Sync + 'static;
    /// The name of the realtime resource the pattern belongs to.
    const REALTIME: &'static str;
    /// The pattern, `/`-separated segments that are each a literal or a `:param`.
    const PATTERN: &'static str;

    #[doc(hidden)]
    fn to_params(&self) -> BTreeMap<String, String>;

    #[doc(hidden)]
    fn from_params(params: &BTreeMap<String, String>) -> Self;
}

#[doc(hidden)]
pub struct DeclaredChannel {
    pub realtime: &'static str,
    pub pattern: &'static str,
    pub wildcard: bool,
    pub public: bool,
    pub publish: bool,
    pub schema: Option<Schema>,
    pub token_ttl: Option<Duration>,
    pub file: &'static str,
    pub line: u32,
}

#[doc(hidden)]
pub struct RegisteredChannel(pub fn() -> DeclaredChannel);

inventory::collect!(RegisteredChannel);

pub(crate) fn list_declared_channels() -> Vec<DeclaredChannel> {
    let mut channels: Vec<DeclaredChannel> = inventory::iter::<RegisteredChannel>
        .into_iter()
        .map(|registered| (registered.0)())
        .collect();
    channels.sort_by_key(|channel| (channel.file, channel.line));
    channels
}

pub(crate) fn resolve_token_ttl(channels: &[DeclaredChannel]) -> Result<Duration, String> {
    let mut chosen: Option<&DeclaredChannel> = None;
    for channel in channels
        .iter()
        .filter(|channel| channel.token_ttl.is_some())
    {
        match chosen {
            Some(prior) if prior.token_ttl != channel.token_ttl => {
                return Err(format!(
                    "sets token_ttl {:?} on channel \"{}\" at {}:{} and {:?} on channel \"{}\" at {}:{}, and a realtime resource has one token TTL: set it on one channel, or the same on each",
                    prior.token_ttl.unwrap_or_default(),
                    prior.pattern,
                    prior.file,
                    prior.line,
                    channel.token_ttl.unwrap_or_default(),
                    channel.pattern,
                    channel.file,
                    channel.line
                ))
            }
            Some(_) => {}
            None => chosen = Some(channel),
        }
    }
    Ok(chosen
        .and_then(|channel| channel.token_ttl)
        .unwrap_or(DEFAULT_TOKEN_TTL))
}

/// The request the realtime handler received, as `authorize` and every rule see it.
#[derive(Clone, Debug)]
pub struct Request {
    /// The HTTP method.
    pub method: http::Method,
    /// The URI the request was made to.
    pub uri: http::Uri,
    /// The request's headers.
    pub headers: http::HeaderMap,
    /// The request's body.
    pub body: Bytes,
}

/// What a subscribe rule decides from.
pub struct SubscribeContext<A, C> {
    /// What `authorize` answered for the request.
    pub auth: Arc<A>,
    /// The params the caller asked for. On a `wildcard` channel, the trailing params a
    /// subscriber left off are empty strings.
    pub params: C,
    /// The request the realtime handler received.
    pub request: Arc<Request>,
}

/// What a publish rule decides from.
pub struct PublishContext<A, C: Channel> {
    /// What `authorize` answered for the request.
    pub auth: Arc<A>,
    /// The params the caller asked for, every one set.
    pub params: C,
    /// The event the caller asked to publish, decoded from the request's JSON.
    pub body: C::Event,
    /// The request the realtime handler received.
    pub request: Arc<Request>,
}

/// A rule deciding whether a caller may subscribe to a channel of `C`'s pattern: any async
/// function or closure taking a [`SubscribeContext`] and answering `Ok(true)` to allow it.
pub trait SubscribeRule<A, C>:
    Fn(SubscribeContext<A, C>) -> Self::Decision + Send + Sync + 'static
{
    /// The future the rule answers with.
    type Decision: Future<Output = Result<bool, Self::Failure>> + Send + 'static;
    /// What the rule fails with, which denies the op with `rule-error`.
    type Failure: Display;
}

impl<A, C, F, Fut, E> SubscribeRule<A, C> for F
where
    F: Fn(SubscribeContext<A, C>) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Result<bool, E>> + Send + 'static,
    E: Display,
{
    type Decision = Fut;
    type Failure = E;
}

/// A rule deciding whether a caller may publish an event on a channel of `C`'s pattern from
/// a browser: any async function or closure taking a [`PublishContext`] and answering
/// `Ok(true)` to allow it.
pub trait PublishRule<A, C: Channel>:
    Fn(PublishContext<A, C>) -> Self::Decision + Send + Sync + 'static
{
    /// The future the rule answers with.
    type Decision: Future<Output = Result<bool, Self::Failure>> + Send + 'static;
    /// What the rule fails with, which denies the op with `rule-error`.
    type Failure: Display;
}

impl<A, C, F, Fut, E> PublishRule<A, C> for F
where
    C: Channel,
    F: Fn(PublishContext<A, C>) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Result<bool, E>> + Send + 'static,
    E: Display,
{
    type Decision = Fut;
    type Failure = E;
}

type ErasedAuth = Arc<dyn Any + Send + Sync>;
type AuthorizeFn = Arc<
    dyn Fn(Arc<Request>) -> BoxFuture<'static, Result<Option<(ErasedAuth, String)>, String>>
        + Send
        + Sync,
>;
type RuleFn = Arc<
    dyn Fn(
            ErasedAuth,
            BTreeMap<String, String>,
            Option<Box<dyn Any + Send>>,
            Arc<Request>,
        ) -> BoxFuture<'static, Result<bool, String>>
        + Send
        + Sync,
>;
type DecodeFn = fn(&serde_json::Value) -> Result<(Box<dyn Any + Send>, serde_json::Value), String>;

fn run_caught<T, Fut>(work: Fut) -> BoxFuture<'static, Result<T, String>>
where
    Fut: Future<Output = Result<T, String>> + Send + 'static,
{
    Box::pin(
        AssertUnwindSafe(work)
            .catch_unwind()
            .map(|outcome| outcome.unwrap_or_else(|_| Err("it panicked".to_string()))),
    )
}

#[derive(Serialize)]
struct Envelope<'a> {
    v: u8,
    id: String,
    ch: &'a str,
    ts: u64,
    kind: &'static str,
    data: serde_json::Value,
}

fn generate_envelope_id() -> Result<String, String> {
    let mut id = [0u8; 16];
    ring::rand::SecureRandom::fill(&ring::rand::SystemRandom::new(), &mut id)
        .map_err(|_| "the system gave no randomness for an event id".to_string())?;
    Ok(id.iter().map(|byte| format!("{byte:02x}")).collect())
}

struct EventSchema {
    schemas: boon::Schemas,
    index: boon::SchemaIndex,
}

fn compile_event_schema(written: &str) -> Result<EventSchema, String> {
    const LOCATION: &str = "urn:ocel:realtime:channel";
    let document = serde_json::from_str(written).map_err(|err| err.to_string())?;
    let mut compiler = boon::Compiler::new();
    compiler.use_loader(Box::new(boon::SchemeUrlLoader::new()));
    compiler
        .add_resource(LOCATION, document)
        .map_err(|err| err.to_string())?;
    let mut schemas = boon::Schemas::new();
    let index = compiler
        .compile(LOCATION, &mut schemas)
        .map_err(|err| err.to_string())?;
    Ok(EventSchema { schemas, index })
}

pub(crate) struct ServedChannel {
    pub(crate) pattern: ChannelPattern<'static>,
    pub(crate) wildcard: bool,
    pub(crate) subscribe: Option<RuleFn>,
    pub(crate) publish: Option<(RuleFn, DecodeFn)>,
    schema: Option<EventSchema>,
}

impl ServedChannel {
    pub(crate) fn encode_envelope(
        &self,
        wire: &str,
        data: serde_json::Value,
        id: String,
    ) -> Result<Bytes, DenialCode> {
        if self
            .schema
            .as_ref()
            .is_some_and(|schema| schema.schemas.validate(&data, schema.index).is_err())
        {
            return Err(DenialCode::InvalidBody);
        }
        let encoded = serde_json::to_vec(&Envelope {
            v: 1,
            id,
            ch: wire,
            ts: std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap_or_default()
                .as_millis() as u64,
            kind: "live",
            data,
        })
        .map_err(|_| DenialCode::InvalidBody)?;
        if encoded.len() > MAX_EVENT_BYTES {
            return Err(DenialCode::BodyTooLarge);
        }
        Ok(Bytes::from(encoded))
    }
}

pub(crate) struct Resource {
    pub(crate) name: String,
    pub(crate) token_ttl: Duration,
    pub(crate) allowed_origins: Vec<String>,
    pub(crate) authorize: Option<AuthorizeFn>,
    pub(crate) channels: HashMap<&'static str, ServedChannel>,
    pub(crate) runtime: OnceLock<RealtimeServiceClient<HttpClient>>,
}

/// A realtime resource whose channels are declared by [`Channel`] structs, built with its
/// rules by [`Realtime::builder`]. Serve it with [`Realtime::handle`], or with
/// `ocel::realtime::axum::router` under the `axum` feature.
#[derive(Clone)]
pub struct Realtime {
    pub(crate) inner: Arc<Resource>,
}

/// Builds a [`Realtime`]: its `authorize`, the rules of its channels and the origins its
/// handler serves.
pub struct RealtimeBuilder<A> {
    name: String,
    allowed_origins: Vec<String>,
    authorize: Option<AuthorizeFn>,
    subscribe: HashMap<&'static str, (&'static str, RuleFn)>,
    publish: HashMap<&'static str, (&'static str, RuleFn, DecodeFn)>,
    has_rules_before_authorize: bool,
    auth: PhantomData<fn() -> A>,
}

fn read_subject<A: Serialize>(auth: &A) -> String {
    match serde_json::to_value(auth)
        .ok()
        .and_then(|value| value.get("id").cloned())
    {
        Some(serde_json::Value::String(id)) => id,
        Some(serde_json::Value::Number(id)) => id.to_string(),
        _ => "anonymous".to_string(),
    }
}

impl Realtime {
    /// Start building the realtime resource named `name`, the name its [`Channel`] structs
    /// give as `realtime`.
    pub fn builder(name: impl Into<String>) -> RealtimeBuilder<()> {
        RealtimeBuilder {
            name: name.into(),
            allowed_origins: Vec::new(),
            authorize: None,
            subscribe: HashMap::new(),
            publish: HashMap::new(),
            has_rules_before_authorize: false,
            auth: PhantomData,
        }
    }

    pub(crate) fn read_properties(&self, access: &str) -> Result<RealtimeProperties, Error> {
        if is_discovering() {
            return Err(Error::Unprovisioned {
                resource: format!("realtime(\"{}\")", self.inner.name),
                access: access.to_string(),
            });
        }
        read_realtime(&self.inner.name)
    }

    /// Publish `event` to every subscriber of the channel `params` fill in `C`'s pattern,
    /// through the ocel runtime. A channel of another resource, an empty param, one over 30
    /// bytes, an event its JSON Schema refuses or whose JSON is over 240 KiB is refused with
    /// [`Error::PublishRefused`]; an event the runtime does not publish fails with
    /// [`Error::PublishFailed`].
    pub async fn publish<C: Channel>(&self, params: &C, event: &C::Event) -> Result<(), Error> {
        let refuse = |code| Error::PublishRefused {
            pattern: C::PATTERN.to_string(),
            code,
        };
        let channel = self
            .inner
            .channels
            .get(C::PATTERN)
            .filter(|_| C::REALTIME == self.inner.name)
            .ok_or_else(|| refuse(DenialCode::UnknownPattern))?;
        self.read_properties("publish")?;
        let failed = |said: String| Error::PublishFailed {
            name: self.inner.name.clone(),
            said,
        };
        let data = serde_json::to_value(event).map_err(|err| Error::Payload {
            said: err.to_string(),
        })?;
        let wire = encode_wire_channel(
            &self.inner.name,
            &channel.pattern,
            &params.to_params(),
            false,
        )
        .map_err(refuse)?;
        let envelope = channel
            .encode_envelope(&wire, data, generate_envelope_id().map_err(failed)?)
            .map_err(refuse)?;
        self.publish_event(&wire, envelope).await
    }

    pub(crate) async fn publish_event(&self, wire: &str, envelope: Bytes) -> Result<(), Error> {
        let failed = |said: String| Error::PublishFailed {
            name: self.inner.name.clone(),
            said: format!("publish on {wire}: {said}"),
        };
        let runtime = match self.inner.runtime.get() {
            Some(runtime) => runtime,
            None => {
                let client =
                    RealtimeServiceClient::new(HttpClient::plaintext(), read_client_config()?);
                self.inner.runtime.get_or_init(|| client)
            }
        };
        let event = String::from_utf8(envelope.to_vec()).map_err(|err| failed(err.to_string()))?;
        runtime
            .publish(PublishRequest {
                realtime: self.inner.name.clone(),
                channel: wire.to_string(),
                event,
                ..Default::default()
            })
            .await
            .map_err(|err| failed(err.to_string()))?;
        Ok(())
    }

    /// Serve one request to the realtime handler: a batched `POST` of `{ connect?, ops }`,
    /// at most 50 ops, answered with the transport, its url, a connect token when asked, a
    /// grant with a token for each op its rule allows and a denial with a [`DenialCode`]
    /// for each it does not. A browser publish runs the channel's publish rule and is
    /// published from the server, in the order of the batch. It takes `POST` with
    /// `application/json` alone, serves its own origin and those the builder allowed (the
    /// only ones it sends CORS headers to), answers `Cache-Control: no-store`, and never
    /// sets a cookie.
    pub async fn handle(&self, request: Request) -> http::Response<Bytes> {
        handler::handle(self, request, false).await
    }
}

impl RealtimeBuilder<()> {
    /// Identify the caller of the realtime handler from its request, with whatever auth the
    /// app already uses, answering `None` for nobody; an error or a panic fails the whole
    /// request with 500. Its answer is the `auth` of every rule, and its `id` as JSON, when
    /// a string or a number, is the `sub` of every token minted for the caller. Call it
    /// before any rule; a resource with any rule needs it.
    pub fn authorize<A, F, Fut, E>(self, authorize: F) -> RealtimeBuilder<A>
    where
        A: Serialize + Send + Sync + 'static,
        F: Fn(Arc<Request>) -> Fut + Send + Sync + 'static,
        Fut: Future<Output = Result<Option<A>, E>> + Send + 'static,
        E: Display,
    {
        let authorize = Arc::new(authorize);
        RealtimeBuilder {
            name: self.name,
            allowed_origins: self.allowed_origins,
            authorize: Some(Arc::new(move |request| {
                let authorize = authorize.clone();
                run_caught(async move {
                    match authorize(request).await {
                        Ok(Some(auth)) => {
                            let subject = read_subject(&auth);
                            Ok(Some((Arc::new(auth) as ErasedAuth, subject)))
                        }
                        Ok(None) => Ok(None),
                        Err(err) => Err(err.to_string()),
                    }
                })
            })),
            has_rules_before_authorize: !self.subscribe.is_empty() || !self.publish.is_empty(),
            subscribe: self.subscribe,
            publish: self.publish,
            auth: PhantomData,
        }
    }
}

impl<A: Send + Sync + 'static> RealtimeBuilder<A> {
    /// Origins besides the handler's own that may call it, such as `https://app.example`.
    /// Each is answered with CORS headers; without any, only the handler's own origin is
    /// served and no CORS header is ever sent.
    pub fn allow_origins<I, S>(mut self, origins: I) -> Self
    where
        I: IntoIterator<Item = S>,
        S: Into<String>,
    {
        self.allowed_origins
            .extend(origins.into_iter().map(Into::into));
        self
    }

    /// The rule deciding whether a caller may subscribe to a channel of `C`'s pattern,
    /// which every channel declared without `public` takes, written
    /// `.subscribe::<Orders>(|ctx| async move { ... })`. It runs only for a caller
    /// `authorize` answered for; a rule that fails or panics denies the op with `rule-error`.
    pub fn subscribe<C: Channel>(mut self, rule: impl SubscribeRule<A, C>) -> Self {
        let rule = Arc::new(rule);
        let erased: RuleFn = Arc::new(move |auth, params, _, request| {
            let rule = rule.clone();
            run_caught(async move {
                let auth = auth
                    .downcast::<A>()
                    .map_err(|_| "the auth is not what the rule takes".to_string())?;
                rule(SubscribeContext {
                    auth,
                    params: C::from_params(&params),
                    request,
                })
                .await
                .map_err(|err| err.to_string())
            })
        });
        self.subscribe.insert(C::PATTERN, (C::REALTIME, erased));
        self
    }

    /// The rule deciding whether a caller may publish an event on a channel of `C`'s
    /// pattern from a browser, which the handler then publishes from the server, written
    /// `.publish::<Rooms>(|ctx| async move { ... })`. Every channel declared with `publish`
    /// takes one; a rule that fails or panics denies the op with `rule-error`.
    pub fn publish<C: Channel>(mut self, rule: impl PublishRule<A, C>) -> Self {
        let rule = Arc::new(rule);
        let erased: RuleFn = Arc::new(move |auth, params, body, request| {
            let rule = rule.clone();
            run_caught(async move {
                let (Ok(auth), Some(Ok(body))) = (
                    auth.downcast::<A>(),
                    body.map(|body| body.downcast::<C::Event>()),
                ) else {
                    return Err("the auth or body is not what the rule takes".to_string());
                };
                rule(PublishContext {
                    auth,
                    params: C::from_params(&params),
                    body: *body,
                    request,
                })
                .await
                .map_err(|err| err.to_string())
            })
        });
        let decode: DecodeFn = |raw| {
            let event: C::Event =
                serde_json::from_value(raw.clone()).map_err(|err| err.to_string())?;
            let data = serde_json::to_value(&event).map_err(|err| err.to_string())?;
            Ok((Box::new(event) as Box<dyn Any + Send>, data))
        };
        self.publish
            .insert(C::PATTERN, (C::REALTIME, erased, decode));
        self
    }

    /// The realtime resource, with every channel declared under its name and the token TTL
    /// they declare. It fails when the name is outside the contract, when a rule came
    /// before `authorize` or `authorize` is missing while any rule is set, when a channel's
    /// rules disagree with its declaration, or when its channels declare different token
    /// TTLs.
    pub fn build(mut self) -> Result<Realtime, Error> {
        let name = self.name.clone();
        let refuse = |detail: String| Error::RealtimeDeclaration {
            name: name.clone(),
            detail,
        };
        if !is_channel_segment(&self.name) {
            return Err(refuse(format!(
                "the name begins every channel, so it is a channel namespace: {SEGMENT_RULE}"
            )));
        }
        if self.has_rules_before_authorize {
            return Err(refuse(
                "authorize comes before every rule, since the rules take what it answers"
                    .to_string(),
            ));
        }
        if self.authorize.is_none() && (!self.subscribe.is_empty() || !self.publish.is_empty()) {
            return Err(refuse(
                "has rules and no authorize: add .authorize(...) before them, since every rule decides from what it answers"
                    .to_string(),
            ));
        }
        for (pattern, (realtime, _)) in &self.subscribe {
            if *realtime != self.name {
                return Err(refuse(format!("has a subscribe rule for channel \"{pattern}\", which belongs to realtime \"{realtime}\"")));
            }
        }
        for (pattern, (realtime, _, _)) in &self.publish {
            if *realtime != self.name {
                return Err(refuse(format!("has a publish rule for channel \"{pattern}\", which belongs to realtime \"{realtime}\"")));
            }
        }

        let declared: Vec<DeclaredChannel> = list_declared_channels()
            .into_iter()
            .filter(|declared| declared.realtime == self.name)
            .collect();
        let token_ttl = resolve_token_ttl(&declared).map_err(refuse)?;
        let mut channels = HashMap::new();
        for declared in declared {
            let site = format!("{}:{}", declared.file, declared.line);
            let subscribe = self
                .subscribe
                .remove(declared.pattern)
                .map(|(_, rule)| rule);
            let publish = self
                .publish
                .remove(declared.pattern)
                .map(|(_, rule, decode)| (rule, decode));
            match (declared.public, subscribe.is_some()) {
                (true, true) => return Err(refuse(format!("has a subscribe rule for channel \"{}\" declared public at {site}", declared.pattern))),
                (false, false) => return Err(refuse(format!("has no subscribe rule for channel \"{}\" declared at {site}: add one with .subscribe::<_>(...), or declare the channel public", declared.pattern))),
                _ => {}
            }
            match (declared.publish, publish.is_some()) {
                (true, false) => return Err(refuse(format!("has no publish rule for channel \"{}\" declared with publish at {site}: add one with .publish::<_>(...)", declared.pattern))),
                (false, true) => return Err(refuse(format!("has a publish rule for channel \"{}\" declared at {site} without publish, so the build would not know browsers may publish on it", declared.pattern))),
                _ => {}
            }
            let schema = declared
                .schema
                .map(|schema| compile_event_schema(&schema()))
                .transpose()
                .map_err(|err| {
                    refuse(format!(
                        "channel \"{}\" declared at {site} has a JSON Schema no validator takes: {err}",
                        declared.pattern
                    ))
                })?;
            if channels
                .insert(
                    declared.pattern,
                    ServedChannel {
                        pattern: ChannelPattern::split(declared.pattern),
                        wildcard: declared.wildcard,
                        subscribe,
                        publish,
                        schema,
                    },
                )
                .is_some()
            {
                return Err(refuse(format!(
                    "declares channel \"{}\" twice",
                    declared.pattern
                )));
            }
        }
        Ok(Realtime {
            inner: Arc::new(Resource {
                name: self.name,
                token_ttl,
                allowed_origins: self.allowed_origins,
                authorize: self.authorize,
                channels,
                runtime: OnceLock::new(),
            }),
        })
    }
}
