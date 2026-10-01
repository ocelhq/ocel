use crate::binding;
use crate::declare::is_discovering;
use crate::json::{convert_timestamp, convert_value};
use crate::payload::{encode_payload, Due};
use crate::proto::app::topic::v1::{
    CountDeadLettersRequest, ListDeadLettersRequest, PurgeDeadLettersRequest,
    RedriveDeadLettersRequest, SendRequest, TopicServiceClient,
};
use crate::run::Message;
use crate::runtime::read_client_config;
use crate::{Error, Lane};
use connectrpc::client::HttpClient;
use serde::Serialize;
use std::future::{Future, IntoFuture};
use std::marker::PhantomData;
use std::pin::Pin;
use std::sync::{Arc, OnceLock};
use std::time::{Duration, SystemTime};

type ResultFuture<'a, T> = Pin<Box<dyn Future<Output = Result<T, Error>> + Send + 'a>>;

struct Connection {
    client: TopicServiceClient<HttpClient>,
    topic: String,
}

/// The name of a topic whose messages are of type `T`. A struct deriving
/// [`Resources`](macro@crate::Resources) holds one as a constant for each of its
/// [`Topic<T>`] fields, named after the field in upper case, so the field `orders` of
/// `Infra` is `Infra::ORDERS`. [`macro@crate::consumer`] and [`macro@crate::batch_consumer`]
/// name the topic they consume with it, and a consumer whose payload is not `T` does not
/// compile.
pub struct TopicName<T> {
    name: &'static str,
    types: PhantomData<fn(T)>,
}

impl<T> TopicName<T> {
    #[doc(hidden)]
    pub const fn new(name: &'static str) -> Self {
        Self {
            name,
            types: PhantomData,
        }
    }

    /// The name the topic was declared under.
    pub const fn name(&self) -> &'static str {
        self.name
    }
}

impl<T> Clone for TopicName<T> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<T> Copy for TopicName<T> {}

/// A topic: messages of type `T` sent to it fan out to every consumer declared on it with
/// [`macro@crate::consumer`] or [`macro@crate::batch_consumer`]. A field of this type in a
/// struct deriving [`Resources`](macro@crate::Resources) is the declaration.
pub struct Topic<T> {
    name: String,
    connection: Arc<OnceLock<Connection>>,
    types: PhantomData<fn(T)>,
}

impl<T> Clone for Topic<T> {
    fn clone(&self) -> Self {
        Self {
            name: self.name.clone(),
            connection: self.connection.clone(),
            types: PhantomData,
        }
    }
}

impl<T> Topic<T> {
    /// Take the handle for the topic named `name`. Prefer
    /// [`Resources`](macro@crate::Resources), which declares the topic as well as handing
    /// back its handle.
    pub fn new(name: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            connection: Arc::default(),
            types: PhantomData,
        }
    }

    /// The name the topic was declared under, and the name its binding is delivered as.
    pub fn name(&self) -> &str {
        &self.name
    }

    /// The messages the consumer named `consumer` gave up on after its last attempt.
    pub fn dead_letter(&self, consumer: &str) -> DeadLetters<'_, T> {
        DeadLetters {
            topic: self,
            consumer: consumer.to_string(),
        }
    }

    fn ensure_connection(&self, access: &str) -> Result<&Connection, Error> {
        if is_discovering() {
            return Err(Error::Unprovisioned {
                resource: self.describe_resource(),
                access: access.to_string(),
            });
        }
        if let Some(connection) = self.connection.get() {
            return Ok(connection);
        }
        let topic = binding::read_topic(&self.name)?;
        let connection = Connection {
            client: TopicServiceClient::new(HttpClient::plaintext(), read_client_config()?),
            topic,
        };
        Ok(self.connection.get_or_init(|| connection))
    }

    fn describe_resource(&self) -> String {
        format!("topic(\"{}\")", self.name)
    }

    fn refuse_access(&self, access: &str, err: &connectrpc::ConnectError) -> Error {
        Error::RuntimeRefused {
            resource: self.describe_resource(),
            access: access.to_string(),
            said: err.to_string(),
        }
    }
}

impl<T: Serialize> Topic<T> {
    /// Send `payload` to every consumer of the topic. The returned builder sets the
    /// message's options, and awaiting it sends the message and answers with its id.
    pub fn send(&self, payload: T) -> TopicSend<'_, T> {
        TopicSend {
            topic: self,
            payload: encode_payload(&payload),
            due: None,
            idempotency_key: String::new(),
            key: String::new(),
            lane: None,
        }
    }
}

/// The message [`Topic::send`] sends, which is sent by awaiting it.
pub struct TopicSend<'a, T> {
    topic: &'a Topic<T>,
    payload: Result<Vec<u8>, Error>,
    due: Option<Due>,
    idempotency_key: String,
    key: String,
    lane: Option<Lane>,
}

impl<T> TopicSend<'_, T> {
    /// Hold the message until `delay` from now, at most 30 days.
    pub fn delay(mut self, delay: Duration) -> Self {
        self.due = Some(Due::In(delay));
        self
    }

    /// Hold the message until `at`, at most 30 days from now.
    pub fn at(mut self, at: SystemTime) -> Self {
        self.due = Some(Due::At(at));
        self
    }

    /// Answer a send that repeats `key` with the message the first one sent, instead of
    /// sending another.
    pub fn idempotency_key(mut self, key: impl Into<String>) -> Self {
        self.idempotency_key = key.into();
        self
    }

    /// On an ordered topic, the key messages are ordered by: messages sharing a key reach
    /// each consumer one at a time, in the order they were sent.
    pub fn key(mut self, key: impl Into<String>) -> Self {
        self.key = key.into();
        self
    }

    /// The lane the message waits in.
    pub fn lane(mut self, lane: Lane) -> Self {
        self.lane = Some(lane);
        self
    }
}

impl<'a, T> IntoFuture for TopicSend<'a, T> {
    type Output = Result<String, Error>;
    type IntoFuture = ResultFuture<'a, String>;

    fn into_future(self) -> Self::IntoFuture {
        let connection = self.topic.ensure_connection("send");
        let resource = self.topic.describe_resource();
        let TopicSend {
            payload,
            due,
            idempotency_key,
            key,
            lane,
            ..
        } = self;
        Box::pin(async move {
            let connection = connection?;
            let response = connection
                .client
                .send(SendRequest {
                    topic: connection.topic.clone(),
                    payload: payload?,
                    due_at: due.map(Due::to_timestamp).into(),
                    idempotency_key,
                    key,
                    lane: lane.map(Lane::to_wire).unwrap_or_default().into(),
                    ..Default::default()
                })
                .await
                .map_err(|err| Error::RuntimeRefused {
                    resource,
                    access: "send".to_string(),
                    said: err.to_string(),
                })?;
            Ok(response.into_owned().message_id)
        })
    }
}

/// One message a consumer gave up on.
#[derive(Clone, Debug, PartialEq)]
pub struct DeadLetter {
    /// The execution: the message as this consumer received it, and the id a redrive or
    /// purge names it by.
    pub execution: String,
    /// The message that was sent.
    pub message: Message,
    /// The message's payload, as JSON.
    pub payload: serde_json::Value,
    /// How many attempts the consumer made.
    pub attempts: u32,
    /// The error the last attempt failed with.
    pub error: String,
    /// When the last attempt failed.
    pub failed_at: Option<SystemTime>,
}

/// One page of a consumer's dead letters.
#[derive(Clone, Debug, PartialEq)]
pub struct DeadLetterPage {
    /// The dead letters on this page.
    pub dead_letters: Vec<DeadLetter>,
    /// The cursor the next page starts at, or `None` on the last page.
    pub next_cursor: Option<String>,
}

/// The dead letters of one consumer of a topic, from [`Topic::dead_letter`].
pub struct DeadLetters<'a, T> {
    topic: &'a Topic<T>,
    consumer: String,
}

impl<T> DeadLetters<'_, T> {
    /// Page through the dead letters. The returned builder bounds the page, and awaiting it
    /// reads one.
    pub fn list(&self) -> DeadLetterList<'_, T> {
        DeadLetterList {
            dead_letters: self,
            cursor: String::new(),
            limit: 0,
        }
    }

    /// Send the dead letters named by their `executions` back to the consumer, and answer
    /// with how many went back.
    pub async fn redrive(
        &self,
        executions: impl IntoIterator<Item = impl Into<String>>,
    ) -> Result<u64, Error> {
        let executions: Vec<String> = executions.into_iter().map(Into::into).collect();
        if executions.is_empty() {
            return Ok(0);
        }
        self.redrive_executions(executions).await
    }

    /// Send every dead letter back to the consumer, and answer with how many went back.
    pub async fn redrive_all(&self) -> Result<u64, Error> {
        self.redrive_executions(Vec::new()).await
    }

    /// Delete the dead letters named by their `executions`, and answer with how many were
    /// deleted.
    pub async fn purge(
        &self,
        executions: impl IntoIterator<Item = impl Into<String>>,
    ) -> Result<u64, Error> {
        let executions: Vec<String> = executions.into_iter().map(Into::into).collect();
        if executions.is_empty() {
            return Ok(0);
        }
        self.purge_executions(executions).await
    }

    /// Delete every dead letter, and answer with how many were deleted.
    pub async fn purge_all(&self) -> Result<u64, Error> {
        self.purge_executions(Vec::new()).await
    }

    /// How many dead letters the consumer has.
    pub async fn count(&self) -> Result<u64, Error> {
        let connection = self.topic.ensure_connection("dead_letter.count")?;
        let response = connection
            .client
            .count_dead_letters(CountDeadLettersRequest {
                topic: connection.topic.clone(),
                consumer: self.consumer.clone(),
                ..Default::default()
            })
            .await
            .map_err(|err| self.topic.refuse_access("dead_letter.count", &err))?;
        Ok(response.into_owned().count.max(0) as u64)
    }

    async fn redrive_executions(&self, executions: Vec<String>) -> Result<u64, Error> {
        let connection = self.topic.ensure_connection("dead_letter.redrive")?;
        let response = connection
            .client
            .redrive_dead_letters(RedriveDeadLettersRequest {
                topic: connection.topic.clone(),
                consumer: self.consumer.clone(),
                executions,
                ..Default::default()
            })
            .await
            .map_err(|err| self.topic.refuse_access("dead_letter.redrive", &err))?;
        Ok(response.into_owned().redriven.max(0) as u64)
    }

    async fn purge_executions(&self, executions: Vec<String>) -> Result<u64, Error> {
        let connection = self.topic.ensure_connection("dead_letter.purge")?;
        let response = connection
            .client
            .purge_dead_letters(PurgeDeadLettersRequest {
                topic: connection.topic.clone(),
                consumer: self.consumer.clone(),
                executions,
                ..Default::default()
            })
            .await
            .map_err(|err| self.topic.refuse_access("dead_letter.purge", &err))?;
        Ok(response.into_owned().purged.max(0) as u64)
    }
}

/// The page [`DeadLetters::list`] reads, which is read by awaiting it.
pub struct DeadLetterList<'a, T> {
    dead_letters: &'a DeadLetters<'a, T>,
    cursor: String,
    limit: i32,
}

impl<T> DeadLetterList<'_, T> {
    /// Start at the cursor a previous page answered with.
    pub fn cursor(mut self, cursor: impl Into<String>) -> Self {
        self.cursor = cursor.into();
        self
    }

    /// Read at most `limit` dead letters, at most 1,000.
    pub fn limit(mut self, limit: u32) -> Self {
        self.limit = limit.min(i32::MAX as u32) as i32;
        self
    }
}

impl<'a, T> IntoFuture for DeadLetterList<'a, T> {
    type Output = Result<DeadLetterPage, Error>;
    type IntoFuture = ResultFuture<'a, DeadLetterPage>;

    fn into_future(self) -> Self::IntoFuture {
        let topic = self.dead_letters.topic;
        let connection = topic.ensure_connection("dead_letter.list");
        let resource = topic.describe_resource();
        let consumer = self.dead_letters.consumer.clone();
        let (cursor, limit) = (self.cursor, self.limit);
        Box::pin(async move {
            let connection = connection?;
            let response = connection
                .client
                .list_dead_letters(ListDeadLettersRequest {
                    topic: connection.topic.clone(),
                    consumer,
                    cursor,
                    limit,
                    ..Default::default()
                })
                .await
                .map_err(|err| Error::RuntimeRefused {
                    resource,
                    access: "dead_letter.list".to_string(),
                    said: err.to_string(),
                })?
                .into_owned();
            Ok(DeadLetterPage {
                dead_letters: response
                    .dead_letters
                    .into_iter()
                    .map(|letter| DeadLetter {
                        message: Message {
                            id: letter
                                .message
                                .as_option()
                                .map(|message| message.id.clone())
                                .unwrap_or_default(),
                            published_at: letter
                                .message
                                .as_option()
                                .and_then(|message| message.published_at.as_option())
                                .and_then(convert_timestamp),
                        },
                        payload: convert_value(letter.payload.as_option()),
                        attempts: letter.attempts.max(0) as u32,
                        failed_at: letter.failed_at.as_option().and_then(convert_timestamp),
                        execution: letter.execution,
                        error: letter.error,
                    })
                    .collect(),
                next_cursor: Some(response.next_cursor).filter(|cursor| !cursor.is_empty()),
            })
        })
    }
}
