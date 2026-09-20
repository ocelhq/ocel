mod writer;

#[cfg(test)]
mod fake;
#[cfg(test)]
mod tests;

use crate::binding::bucket;
use crate::declare::discovering;
use crate::proto::app::bucket::v1::{
    BucketServiceClient, CopyRequest, DeleteRequest, HeadRequest, ListRequest, ObjectInfo,
    PresignedTarget, SignConstraints, SignRequest, SignedAudience, SignedOperation,
};
use crate::Error;
use bytes::Bytes;
use connectrpc::client::{ClientConfig, ClientTransport, HttpClient};
use futures_core::Stream;
use http_body_util::BodyExt;
use std::collections::BTreeMap;
use std::future::{Future, IntoFuture};
use std::ops::{Bound, RangeBounds};
use std::pin::Pin;
use std::sync::{Arc, OnceLock};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

pub use writer::Writer;

pub(crate) const KIND: &str = "bucket";

const RUNTIME_ADDRESS_ENV: &str = "OCEL_RUNTIME_ADDRESS";
const SESSION_TOKEN_ENV: &str = "OCEL_SESSION_TOKEN";
const SINGLE_REQUEST_CEILING: usize = 16 << 20;
const PART_SIZE: usize = 8 << 20;
const PARTS_IN_FLIGHT: usize = 4;

type Body = <HttpClient as ClientTransport>::ResponseBody;
type Client = BucketServiceClient<HttpClient>;
type Eventually<'a, T> = Pin<Box<dyn Future<Output = Result<T, Error>> + Send + 'a>>;
type Arriving = futures_util::stream::MapErr<
    http_body_util::BodyDataStream<Body>,
    fn(<Body as connectrpc::http_body::Body>::Error) -> std::io::Error,
>;

/// What a bucket knows about one object it holds.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Object {
    /// The key the object is addressed by.
    pub key: String,
    /// The object's length in bytes.
    pub size: u64,
    /// The store's opaque version tag for these bytes.
    pub etag: String,
    /// The media type the object was written with.
    pub content_type: String,
    /// When the object last took its current bytes, as the store reports it.
    pub uploaded_at: Option<SystemTime>,
    /// The user metadata written alongside the object.
    pub metadata: BTreeMap<String, String>,
}

/// A target someone without a credential writes one object through, for as long as it stays
/// valid.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SignedUpload {
    /// Where the body is sent.
    pub url: String,
    /// The HTTP method to send it with.
    pub method: String,
    /// The headers the signature covers, which the caller must send unchanged.
    pub headers: BTreeMap<String, String>,
    /// The form fields a POST target carries, which are empty for a PUT target.
    pub fields: BTreeMap<String, String>,
}

/// The bytes of one object, however the caller happened to be holding them.
#[derive(Clone, Debug, Default)]
pub struct Payload(Bytes);

impl Payload {
    /// The bytes themselves.
    pub fn into_bytes(self) -> Bytes {
        self.0
    }
}

impl From<Bytes> for Payload {
    fn from(bytes: Bytes) -> Self {
        Self(bytes)
    }
}

impl From<Vec<u8>> for Payload {
    fn from(bytes: Vec<u8>) -> Self {
        Self(Bytes::from(bytes))
    }
}

impl From<&'static [u8]> for Payload {
    fn from(bytes: &'static [u8]) -> Self {
        Self(Bytes::from_static(bytes))
    }
}

impl From<String> for Payload {
    fn from(text: String) -> Self {
        Self(Bytes::from(text.into_bytes()))
    }
}

impl From<&str> for Payload {
    fn from(text: &str) -> Self {
        Self(Bytes::copy_from_slice(text.as_bytes()))
    }
}

#[derive(Clone)]
struct Reached {
    client: Client,
    http: HttpClient,
    bucket: String,
    public_base_url: String,
}

#[derive(Clone, Copy)]
pub(crate) struct Thresholds {
    pub(crate) single_ceiling: usize,
    pub(crate) part_size: usize,
}

/// A bucket an app declares and reads and writes its objects through. A field of this type
/// in a struct deriving [`Resources`](macro@crate::Resources) is the declaration.
#[derive(Clone)]
pub struct Bucket {
    name: String,
    reached: Arc<OnceLock<Reached>>,
    thresholds: Thresholds,
}

impl Bucket {
    /// Take the handle for the bucket named `name`. Prefer
    /// [`Resources`](macro@crate::Resources), which declares the bucket as well as handing
    /// back its handle.
    pub fn new(name: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            reached: Arc::default(),
            thresholds: Thresholds {
                single_ceiling: SINGLE_REQUEST_CEILING,
                part_size: PART_SIZE,
            },
        }
    }

    /// The name the bucket was declared under, and the name its binding is delivered as.
    pub fn name(&self) -> &str {
        &self.name
    }

    /// Write `body` as the whole object under `key`. The returned builder describes and
    /// conditions the write, and awaiting it performs one.
    pub fn put(&self, key: &str, body: impl Into<Payload>) -> Put<'_> {
        Put {
            bucket: self,
            key: key.to_string(),
            body: body.into().into_bytes(),
            options: Written::default(),
        }
    }

    /// Read the object under `key`. The returned builder bounds the read, and awaiting it
    /// opens one, failing with [`Error::NotFound`] when the bucket holds no such object.
    pub fn get(&self, key: &str) -> Get<'_> {
        Get {
            bucket: self,
            key: key.to_string(),
            range: None,
        }
    }

    /// What the bucket knows about the object under `key`, or `None` when it holds none.
    pub async fn head(&self, key: &str) -> Result<Option<Object>, Error> {
        let reached = self.reached("head")?;
        let response = reached
            .client
            .head(HeadRequest {
                bucket: reached.bucket.clone(),
                key: key.to_string(),
                ..Default::default()
            })
            .await
            .map_err(|err| refused(key, &err))?;
        Ok(response.into_owned().object.into_option().map(object))
    }

    /// Whether the bucket holds an object under `key`.
    pub async fn exists(&self, key: &str) -> Result<bool, Error> {
        Ok(self.head(key).await?.is_some())
    }

    /// Remove the object under `key`. A key the bucket does not hold is not an error.
    pub async fn delete(&self, key: &str) -> Result<(), Error> {
        self.delete_many([key]).await
    }

    /// Remove the objects under `keys`. A key the bucket does not hold is not an error.
    pub async fn delete_many(
        &self,
        keys: impl IntoIterator<Item = impl AsRef<str>>,
    ) -> Result<(), Error> {
        let keys: Vec<String> = keys
            .into_iter()
            .map(|key| key.as_ref().to_string())
            .collect();
        if keys.is_empty() {
            return Ok(());
        }
        let reached = self.reached("delete")?;
        let named = keys.join(", ");
        reached
            .client
            .delete(DeleteRequest {
                bucket: reached.bucket.clone(),
                keys,
                ..Default::default()
            })
            .await
            .map_err(|err| refused(&named, &err))?;
        Ok(())
    }

    /// Copy the object under `source` to `destination` within the same bucket. It fails
    /// with [`Error::NotFound`] when the bucket holds nothing under `source`.
    pub async fn copy(&self, source: &str, destination: &str) -> Result<Object, Error> {
        let reached = self.reached("copy")?;
        let response = reached
            .client
            .copy(CopyRequest {
                bucket: reached.bucket.clone(),
                source_key: source.to_string(),
                destination_key: destination.to_string(),
                ..Default::default()
            })
            .await
            .map_err(|err| refused(source, &err))?;
        response
            .into_owned()
            .object
            .into_option()
            .map(object)
            .ok_or_else(|| Error::NotFound {
                key: source.to_string(),
            })
    }

    /// Walk the objects the bucket holds. The returned builder bounds the walk, and its
    /// stream pages through the store for as long as it is polled.
    pub fn list(&self) -> List<'_> {
        List {
            bucket: self,
            prefix: String::new(),
            limit: 0,
        }
    }

    /// Open the object under `key` for reading. It fails with [`Error::NotFound`] when the
    /// bucket holds none.
    pub async fn reader(&self, key: &str) -> Result<Reader, Error> {
        let got = self.get(key).await?;
        Ok(Reader {
            inner: tokio_util::io::StreamReader::new(futures_util::TryStreamExt::map_err(
                http_body_util::BodyDataStream::new(got.body),
                |err| std::io::Error::other(err.to_string()),
            )),
        })
    }

    /// Open the object under `key` for writing. Nothing reaches the bucket until the writer
    /// is shut down, and a writer dropped before that throws away what it had sent.
    pub async fn writer(&self, key: &str) -> Result<Writer, Error> {
        Writer::open(self, key, Written::default()).await
    }

    /// A url that reads the object under `key` without a credential, for as long as it
    /// stays valid.
    pub fn signed_url(&self, key: &str) -> Sign<'_> {
        Sign {
            bucket: self,
            key: key.to_string(),
            expires_in: None,
            download: String::new(),
        }
    }

    /// A target that writes the object under `key` without a credential, for as long as it
    /// stays valid.
    pub fn signed_upload(&self, key: &str) -> SignUpload<'_> {
        SignUpload {
            bucket: self,
            key: key.to_string(),
            expires_in: None,
            max_size: 0,
            content_type: String::new(),
        }
    }

    /// The address the object under `key` is served at anonymously. It fails on a bucket
    /// that carries no public address.
    pub fn public_url(&self, key: &str) -> Result<String, Error> {
        let reached = self.reached("public_url")?;
        if reached.public_base_url.is_empty() {
            return Err(Error::NotPublic {
                key: key.to_string(),
            });
        }
        Ok(format!(
            "{}/{key}",
            reached.public_base_url.trim_end_matches('/')
        ))
    }

    #[cfg(test)]
    pub(crate) fn with_thresholds(mut self, single_ceiling: usize, part_size: usize) -> Self {
        self.thresholds = Thresholds {
            single_ceiling,
            part_size,
        };
        self
    }

    fn reached(&self, access: &str) -> Result<&Reached, Error> {
        if discovering() {
            return Err(Error::Unprovisioned {
                resource: format!("bucket(\"{}\")", self.name),
                access: access.to_string(),
            });
        }
        if let Some(reached) = self.reached.get() {
            return Ok(reached);
        }
        let properties = bucket(&self.name)?;
        let address = std::env::var(RUNTIME_ADDRESS_ENV).unwrap_or_default();
        if address.is_empty() {
            return Err(Error::UnreachableRuntime);
        }
        let Ok(base) = address.trim_end_matches('/').parse() else {
            return Err(Error::RuntimeAddress { address });
        };
        let token = std::env::var(SESSION_TOKEN_ENV).unwrap_or_default();
        if token.is_empty() {
            return Err(Error::UntrustedRuntime);
        }
        let http = HttpClient::plaintext();
        let opened = Reached {
            client: Client::new(
                http.clone(),
                ClientConfig::new(base)
                    .with_default_header("authorization", format!("Bearer {token}")),
            ),
            http,
            bucket: properties.bucket,
            public_base_url: properties.public_base_url,
        };
        Ok(self.reached.get_or_init(|| opened))
    }

    async fn sign(&self, access: &str, signed: Signed<'_>) -> Result<PresignedTarget, Error> {
        let reached = self.reached(access)?;
        let response = reached
            .client
            .sign(SignRequest {
                bucket: reached.bucket.clone(),
                key: signed.key.to_string(),
                operation: signed.operation.into(),
                audience: signed.audience.into(),
                expires_in: signed
                    .expires_in
                    .map(|expires| buffa_types::google::protobuf::Duration {
                        seconds: expires.as_secs() as i64,
                        nanos: expires.subsec_nanos() as i32,
                        ..Default::default()
                    })
                    .into(),
                constraints: signed.constraints.into(),
                ..Default::default()
            })
            .await
            .map_err(|err| refused(signed.key, &err))?;
        response
            .into_owned()
            .target
            .into_option()
            .ok_or_else(|| Error::Refused {
                key: signed.key.to_string(),
                said: "the runtime signed nothing".to_string(),
            })
    }
}

struct Signed<'a> {
    key: &'a str,
    operation: SignedOperation,
    audience: SignedAudience,
    expires_in: Option<Duration>,
    constraints: Option<SignConstraints>,
}

#[derive(Clone, Default)]
struct Written {
    content_type: String,
    cache_control: String,
    metadata: BTreeMap<String, String>,
    if_none_match: String,
    if_match: String,
}

/// The write [`Bucket::put`] opens, which is performed by awaiting it.
pub struct Put<'a> {
    bucket: &'a Bucket,
    key: String,
    body: Bytes,
    options: Written,
}

impl Put<'_> {
    /// The media type the object is written under.
    pub fn content_type(mut self, media_type: &str) -> Self {
        self.options.content_type = media_type.to_string();
        self
    }

    /// The cache-control the store serves the object with.
    pub fn cache_control(mut self, value: &str) -> Self {
        self.options.cache_control = value.to_string();
        self
    }

    /// The user metadata kept beside the object, capped at 2 KB.
    pub fn metadata(
        mut self,
        entries: impl IntoIterator<Item = (impl Into<String>, impl Into<String>)>,
    ) -> Self {
        self.options.metadata = entries
            .into_iter()
            .map(|(name, value)| (name.into(), value.into()))
            .collect();
        self
    }

    /// Write only when the bucket holds no object under the key, and fail with
    /// [`Error::PreconditionFailed`] when it does.
    pub fn if_not_exists(mut self) -> Self {
        self.options.if_none_match = "*".to_string();
        self
    }

    /// Write only when the object under the key still carries `etag`, and fail with
    /// [`Error::PreconditionFailed`] when it does not.
    pub fn if_match(mut self, etag: &str) -> Self {
        self.options.if_match = etag.to_string();
        self
    }
}

impl<'a> IntoFuture for Put<'a> {
    type Output = Result<Object, Error>;
    type IntoFuture = Eventually<'a, Object>;

    fn into_future(self) -> Self::IntoFuture {
        Box::pin(async move {
            let Put {
                bucket,
                key,
                body,
                options,
            } = self;
            if body.len() > bucket.thresholds.single_ceiling {
                let mut writer = Writer::open(bucket, &key, options).await?;
                writer.put(body).await?;
                return writer.settle().await;
            }
            let target = bucket
                .sign(
                    "put",
                    Signed {
                        key: &key,
                        operation: SignedOperation::Put,
                        audience: SignedAudience::Internal,
                        expires_in: None,
                        constraints: Some(SignConstraints {
                            content_type: options.content_type.clone(),
                            if_none_match: options.if_none_match.clone(),
                            if_match: options.if_match.clone(),
                            ..Default::default()
                        }),
                    },
                )
                .await?;
            let mut request = signed_request(&target, "PUT");
            for (name, value) in [
                ("content-type", &options.content_type),
                ("cache-control", &options.cache_control),
                ("if-none-match", &options.if_none_match),
                ("if-match", &options.if_match),
            ] {
                if !value.is_empty() {
                    request = request.header(name, value);
                }
            }
            for (name, value) in &options.metadata {
                request = request.header(format!("x-amz-meta-{name}"), value);
            }
            let response = send(bucket.reached("put")?, request, body).await?;
            refusal(&key, response.status().as_u16())?;
            bucket
                .head(&key)
                .await?
                .ok_or_else(|| Error::NotFound { key })
        })
    }
}

/// The read [`Bucket::get`] opens, which is performed by awaiting it.
pub struct Get<'a> {
    bucket: &'a Bucket,
    key: String,
    range: Option<String>,
}

impl Get<'_> {
    /// Read only the bytes `range` covers, rather than the whole object.
    pub fn range(mut self, range: impl RangeBounds<u64>) -> Self {
        let start = match range.start_bound() {
            Bound::Included(&at) => at,
            Bound::Excluded(&at) => at + 1,
            Bound::Unbounded => 0,
        };
        self.range = Some(match range.end_bound() {
            Bound::Included(&at) => format!("bytes={start}-{at}"),
            Bound::Excluded(&at) => format!("bytes={start}-{}", at.saturating_sub(1)),
            Bound::Unbounded => format!("bytes={start}-"),
        });
        self
    }
}

impl<'a> IntoFuture for Get<'a> {
    type Output = Result<GetResult, Error>;
    type IntoFuture = Eventually<'a, GetResult>;

    fn into_future(self) -> Self::IntoFuture {
        Box::pin(async move {
            let Get { bucket, key, range } = self;
            let info = bucket.head(&key).await?.ok_or_else(|| Error::NotFound {
                key: key.to_string(),
            })?;
            let target = bucket
                .sign(
                    "get",
                    Signed {
                        key: &key,
                        operation: SignedOperation::Get,
                        audience: SignedAudience::Internal,
                        expires_in: None,
                        constraints: None,
                    },
                )
                .await?;
            let mut request = signed_request(&target, "GET");
            if let Some(range) = &range {
                request = request.header("range", range);
            }
            let response = send(bucket.reached("get")?, request, Bytes::new()).await?;
            refusal(&key, response.status().as_u16())?;
            Ok(GetResult {
                info,
                key,
                body: response.into_body(),
            })
        })
    }
}

/// An open read of one object: its bytes, and what the bucket knew about it when the read
/// opened.
pub struct GetResult {
    info: Object,
    key: String,
    body: Body,
}

impl GetResult {
    /// What the bucket knew about the whole object when the read opened, whatever range
    /// this read covers.
    pub fn info(&self) -> &Object {
        &self.info
    }

    /// Every byte this read covers, held in memory at once.
    pub async fn bytes(self) -> Result<Bytes, Error> {
        let key = self.key;
        Ok(self
            .body
            .collect()
            .await
            .map_err(|err| Error::Refused {
                key,
                said: err.to_string(),
            })?
            .to_bytes())
    }

    /// The bytes this read covers, as they arrive.
    pub fn into_stream(self) -> impl Stream<Item = Result<Bytes, Error>> {
        let key = self.key;
        futures_util::TryStreamExt::map_err(
            http_body_util::BodyDataStream::new(self.body),
            move |err| Error::Refused {
                key: key.clone(),
                said: err.to_string(),
            },
        )
    }
}

/// One object's bytes, read as they arrive.
pub struct Reader {
    inner: tokio_util::io::StreamReader<Arriving, Bytes>,
}

impl tokio::io::AsyncRead for Reader {
    fn poll_read(
        mut self: Pin<&mut Self>,
        context: &mut std::task::Context<'_>,
        buffer: &mut tokio::io::ReadBuf<'_>,
    ) -> std::task::Poll<std::io::Result<()>> {
        Pin::new(&mut self.inner).poll_read(context, buffer)
    }
}

/// The walk [`Bucket::list`] opens, which is performed by streaming it.
pub struct List<'a> {
    bucket: &'a Bucket,
    prefix: String,
    limit: i32,
}

impl<'a> List<'a> {
    /// Walk only the keys that start with `prefix`.
    pub fn prefix(mut self, prefix: &str) -> Self {
        self.prefix = prefix.to_string();
        self
    }

    /// How many objects one page of the walk carries, at most 1000. The walk itself is not
    /// bounded by it.
    pub fn limit(mut self, objects: u16) -> Self {
        self.limit = i32::from(objects);
        self
    }

    /// The objects the walk covers, a page at a time, ending at the first error.
    pub fn into_stream(self) -> impl Stream<Item = Result<Object, Error>> + 'a {
        let List {
            bucket,
            prefix,
            limit,
        } = self;
        let pages = futures_util::stream::try_unfold(Some(String::new()), move |cursor| {
            let prefix = prefix.clone();
            async move {
                let Some(cursor) = cursor else {
                    return Ok::<_, Error>(None);
                };
                let reached = bucket.reached("list")?;
                let response = reached
                    .client
                    .list(ListRequest {
                        bucket: reached.bucket.clone(),
                        prefix: prefix.clone(),
                        cursor,
                        limit,
                        ..Default::default()
                    })
                    .await
                    .map_err(|err| refused(&prefix, &err))?
                    .into_owned();
                let next = (!response.next_cursor.is_empty()).then_some(response.next_cursor);
                let page: Vec<Object> = response.objects.into_iter().map(object).collect();
                Ok(Some((
                    futures_util::stream::iter(page.into_iter().map(Ok)),
                    next,
                )))
            }
        });
        futures_util::TryStreamExt::try_flatten(pages)
    }
}

/// The signature [`Bucket::signed_url`] mints, which is taken by awaiting it.
pub struct Sign<'a> {
    bucket: &'a Bucket,
    key: String,
    expires_in: Option<Duration>,
    download: String,
}

impl Sign<'_> {
    /// How long the url stays valid. Left out, the runtime picks the lifetime.
    pub fn expires_in(mut self, lifetime: Duration) -> Self {
        self.expires_in = Some(lifetime);
        self
    }

    /// Serve what the url reads as an attachment under this filename.
    pub fn download(mut self, filename: &str) -> Self {
        self.download = filename.to_string();
        self
    }
}

impl<'a> IntoFuture for Sign<'a> {
    type Output = Result<String, Error>;
    type IntoFuture = Eventually<'a, String>;

    fn into_future(self) -> Self::IntoFuture {
        Box::pin(async move {
            let target = self
                .bucket
                .sign(
                    "signed_url",
                    Signed {
                        key: &self.key,
                        operation: SignedOperation::Get,
                        audience: SignedAudience::External,
                        expires_in: self.expires_in,
                        constraints: Some(SignConstraints {
                            download_filename: self.download.clone(),
                            ..Default::default()
                        }),
                    },
                )
                .await?;
            Ok(target.url)
        })
    }
}

/// The target [`Bucket::signed_upload`] mints, which is taken by awaiting it.
pub struct SignUpload<'a> {
    bucket: &'a Bucket,
    key: String,
    expires_in: Option<Duration>,
    max_size: i64,
    content_type: String,
}

impl SignUpload<'_> {
    /// How long the target stays valid. Left out, the runtime picks the lifetime.
    pub fn expires_in(mut self, lifetime: Duration) -> Self {
        self.expires_in = Some(lifetime);
        self
    }

    /// The largest body the target accepts, in bytes.
    pub fn max_size(mut self, bytes: u64) -> Self {
        self.max_size = bytes as i64;
        self
    }

    /// The only media type the target accepts.
    pub fn content_type(mut self, media_type: &str) -> Self {
        self.content_type = media_type.to_string();
        self
    }
}

impl<'a> IntoFuture for SignUpload<'a> {
    type Output = Result<SignedUpload, Error>;
    type IntoFuture = Eventually<'a, SignedUpload>;

    fn into_future(self) -> Self::IntoFuture {
        Box::pin(async move {
            let target = self
                .bucket
                .sign(
                    "signed_upload",
                    Signed {
                        key: &self.key,
                        operation: SignedOperation::PostUpload,
                        audience: SignedAudience::External,
                        expires_in: self.expires_in,
                        constraints: Some(SignConstraints {
                            content_type: self.content_type.clone(),
                            max_size: self.max_size,
                            ..Default::default()
                        }),
                    },
                )
                .await?;
            Ok(SignedUpload {
                method: match target.method.is_empty() {
                    true => "POST".to_string(),
                    false => target.method,
                },
                url: target.url,
                headers: target.headers.into_iter().collect(),
                fields: target.fields.into_iter().collect(),
            })
        })
    }
}

fn signed_request(target: &PresignedTarget, method: &str) -> http::request::Builder {
    let mut request = http::Request::builder().method(method).uri(&target.url);
    for (name, value) in &target.headers {
        request = request.header(name, value);
    }
    request
}

async fn send(
    reached: &Reached,
    request: http::request::Builder,
    body: Bytes,
) -> Result<http::Response<Body>, Error> {
    let key = request
        .uri_ref()
        .map(ToString::to_string)
        .unwrap_or_default();
    let request = request
        .header("content-length", body.len())
        .body(connectrpc::client::full_body(body))
        .map_err(|err| Error::Refused {
            key: key.clone(),
            said: err.to_string(),
        })?;
    reached
        .http
        .send(request)
        .await
        .map_err(|err| Error::Refused {
            key,
            said: err.to_string(),
        })
}

fn refusal(key: &str, status: u16) -> Result<(), Error> {
    match status {
        200..=299 => Ok(()),
        404 => Err(Error::NotFound {
            key: key.to_string(),
        }),
        409 | 412 => Err(Error::PreconditionFailed {
            key: key.to_string(),
        }),
        other => Err(Error::Refused {
            key: key.to_string(),
            said: format!("the store answered {other}"),
        }),
    }
}

fn refused(key: &str, err: &connectrpc::ConnectError) -> Error {
    match err.code {
        connectrpc::ErrorCode::NotFound => Error::NotFound {
            key: key.to_string(),
        },
        connectrpc::ErrorCode::FailedPrecondition => Error::PreconditionFailed {
            key: key.to_string(),
        },
        _ => Error::Refused {
            key: key.to_string(),
            said: err.to_string(),
        },
    }
}

fn object(info: ObjectInfo) -> Object {
    Object {
        key: info.key,
        size: info.size.max(0) as u64,
        etag: info.etag,
        content_type: info.content_type,
        uploaded_at: info.uploaded_at.into_option().and_then(|at| {
            u64::try_from(at.seconds)
                .ok()
                .map(|seconds| UNIX_EPOCH + Duration::new(seconds, at.nanos.max(0) as u32))
        }),
        metadata: info.metadata.into_iter().collect(),
    }
}
