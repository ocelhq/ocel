use super::{
    object, refusal, refused, send, signed_request, Bucket, Object, Reached, Thresholds, Written,
    PARTS_IN_FLIGHT,
};
use crate::proto::app::bucket::v1::{
    AbortMultipartRequest, CompleteMultipartRequest, CompletedPart, CreateMultipartRequest,
    HeadRequest, SignConstraints, SignPartsRequest, SignRequest, SignedAudience, SignedOperation,
};
use crate::Error;
use bytes::Bytes;
use std::future::Future;
use std::pin::Pin;
use std::sync::{Arc, Mutex, MutexGuard};
use std::task::{Context, Poll};

/// One object's bytes, written as they are produced. A small body goes up in a single
/// request; a body that outgrows what one request carries is uploaded in parts, which
/// shutting the writer down settles and dropping it throws away. Nothing is stored until
/// the shutdown returns without an error.
pub struct Writer {
    state: State,
    leash: Arc<Leash>,
}

struct Leash {
    reached: Reached,
    key: String,
    upload_id: Mutex<String>,
}

impl Leash {
    fn upload_id(&self) -> String {
        self.held().clone()
    }

    fn held(&self) -> MutexGuard<'_, String> {
        self.upload_id
            .lock()
            .unwrap_or_else(|held| held.into_inner())
    }
}

enum State {
    Ready(Box<Core>),
    Draining(Working),
    Finishing(Working),
    Spent,
}

type Working = Pin<Box<dyn Future<Output = (Box<Core>, Result<Option<Object>, Error>)> + Send>>;

struct Core {
    reached: Reached,
    key: String,
    options: Written,
    thresholds: Thresholds,
    buffered: Vec<u8>,
    leash: Arc<Leash>,
    next_part: i32,
    pending: Vec<(i32, Bytes)>,
    completed: Vec<CompletedPart>,
}

fn settled() -> Error {
    Error::Refused {
        key: String::new(),
        said: "this writer has already been settled".to_string(),
    }
}

impl Writer {
    pub(super) async fn open(bucket: &Bucket, key: &str, options: Written) -> Result<Self, Error> {
        let reached = bucket.reached("writer")?.clone();
        let leash = Arc::new(Leash {
            reached: reached.clone(),
            key: key.to_string(),
            upload_id: Mutex::new(String::new()),
        });
        Ok(Self {
            state: State::Ready(Box::new(Core {
                reached,
                key: key.to_string(),
                options,
                thresholds: bucket.thresholds,
                buffered: Vec::new(),
                leash: leash.clone(),
                next_part: 1,
                pending: Vec::new(),
                completed: Vec::new(),
            })),
            leash,
        })
    }

    pub(super) async fn put(&mut self, body: Bytes) -> Result<(), Error> {
        let outcome = {
            let State::Ready(core) = &mut self.state else {
                return Err(settled());
            };
            core.buffered.extend_from_slice(&body);
            core.drain(false).await
        };
        let Err(err) = outcome else {
            return Ok(());
        };
        if let State::Ready(core) = &mut self.state {
            core.abort().await;
        }
        self.state = State::Spent;
        Err(err)
    }

    pub(super) async fn settle(mut self) -> Result<Object, Error> {
        let State::Ready(mut core) = std::mem::replace(&mut self.state, State::Spent) else {
            return Err(settled());
        };
        match core.finish().await {
            Ok(Some(held)) => Ok(held),
            Ok(None) => head(&core.reached, &core.key).await,
            Err(err) => {
                core.abort().await;
                Err(err)
            }
        }
    }
}

async fn head(reached: &Reached, key: &str) -> Result<Object, Error> {
    let response = reached
        .client
        .head(HeadRequest {
            bucket: reached.bucket.clone(),
            key: key.to_string(),
            ..Default::default()
        })
        .await
        .map_err(|err| refused(key, &err))?;
    response
        .into_owned()
        .object
        .into_option()
        .map(object)
        .ok_or_else(|| Error::NotFound {
            key: key.to_string(),
        })
}

impl Core {
    async fn drain(&mut self, last: bool) -> Result<(), Error> {
        if self.leash.upload_id().is_empty() {
            if last || self.buffered.len() <= self.thresholds.single_ceiling {
                return Ok(());
            }
            self.begin().await?;
        }
        while self.buffered.len() >= self.thresholds.part_size {
            let part = Bytes::copy_from_slice(&self.buffered[..self.thresholds.part_size]);
            self.buffered.drain(..self.thresholds.part_size);
            self.stage(part);
            if self.pending.len() == PARTS_IN_FLIGHT {
                self.flush().await?;
            }
        }
        if !last {
            return Ok(());
        }
        if !self.buffered.is_empty() {
            let rest = Bytes::from(std::mem::take(&mut self.buffered));
            self.stage(rest);
        }
        self.flush().await
    }

    async fn finish(&mut self) -> Result<Option<Object>, Error> {
        if self.leash.upload_id().is_empty()
            && self.buffered.len() <= self.thresholds.single_ceiling
        {
            self.put_whole().await?;
            return Ok(None);
        }
        self.drain(true).await?;
        self.complete().await.map(Some)
    }

    fn stage(&mut self, part: Bytes) {
        self.pending.push((self.next_part, part));
        self.next_part += 1;
    }

    async fn begin(&mut self) -> Result<(), Error> {
        let response = self
            .reached
            .client
            .create_multipart(CreateMultipartRequest {
                bucket: self.reached.bucket.clone(),
                key: self.key.clone(),
                content_type: self.options.content_type.clone(),
                cache_control: self.options.cache_control.clone(),
                metadata: self
                    .options
                    .metadata
                    .iter()
                    .map(|(name, value)| (name.clone(), value.clone()))
                    .collect(),
                ..Default::default()
            })
            .await
            .map_err(|err| refused(&self.key, &err))?;
        *self.leash.held() = response.into_owned().upload_id;
        Ok(())
    }

    async fn flush(&mut self) -> Result<(), Error> {
        let staged = std::mem::take(&mut self.pending);
        if staged.is_empty() {
            return Ok(());
        }
        let response = self
            .reached
            .client
            .sign_parts(SignPartsRequest {
                bucket: self.reached.bucket.clone(),
                key: self.key.clone(),
                upload_id: self.leash.upload_id(),
                part_numbers: staged.iter().map(|(number, _)| *number).collect(),
                audience: SignedAudience::Internal.into(),
                ..Default::default()
            })
            .await
            .map_err(|err| refused(&self.key, &err))?;
        let targets = response.into_owned().parts;

        let (reached, key) = (&self.reached, self.key.as_str());
        let sending = staged.into_iter().map(|(number, part)| {
            let target = targets.iter().find(|target| target.part_number == number);
            async move {
                let target = target.ok_or_else(|| Error::Refused {
                    key: key.to_string(),
                    said: format!("the runtime signed no url for part {number}"),
                })?;
                let mut request = http::Request::builder().method("PUT").uri(&target.url);
                for (name, value) in &target.headers {
                    request = request.header(name, value);
                }
                let response = send(reached, request, part).await?;
                refusal(key, response.status().as_u16())?;
                Ok::<_, Error>(CompletedPart {
                    part_number: number,
                    etag: response
                        .headers()
                        .get("etag")
                        .and_then(|value| value.to_str().ok())
                        .unwrap_or_default()
                        .to_string(),
                    ..Default::default()
                })
            }
        });
        let done = futures_util::future::try_join_all(sending).await?;
        self.completed.extend(done);
        Ok(())
    }

    async fn complete(&mut self) -> Result<Object, Error> {
        self.completed.sort_by_key(|part| part.part_number);
        let response = self
            .reached
            .client
            .complete_multipart(CompleteMultipartRequest {
                bucket: self.reached.bucket.clone(),
                key: self.key.clone(),
                upload_id: self.leash.upload_id(),
                parts: self.completed.clone(),
                if_none_match: self.options.if_none_match.clone(),
                if_match: self.options.if_match.clone(),
                ..Default::default()
            })
            .await
            .map_err(|err| refused(&self.key, &err))?;
        self.leash.held().clear();
        self.completed.clear();
        response
            .into_owned()
            .object
            .into_option()
            .map(object)
            .ok_or_else(|| Error::NotFound {
                key: self.key.clone(),
            })
    }

    async fn put_whole(&mut self) -> Result<(), Error> {
        let response = self
            .reached
            .client
            .sign(SignRequest {
                bucket: self.reached.bucket.clone(),
                key: self.key.clone(),
                operation: SignedOperation::Put.into(),
                audience: SignedAudience::Internal.into(),
                constraints: Some(SignConstraints {
                    content_type: self.options.content_type.clone(),
                    if_none_match: self.options.if_none_match.clone(),
                    if_match: self.options.if_match.clone(),
                    cache_control: self.options.cache_control.clone(),
                    metadata: self
                        .options
                        .metadata
                        .iter()
                        .map(|(name, value)| (name.clone(), value.clone()))
                        .collect(),
                    ..Default::default()
                })
                .into(),
                ..Default::default()
            })
            .await
            .map_err(|err| refused(&self.key, &err))?;
        let target = response
            .into_owned()
            .target
            .into_option()
            .ok_or_else(|| Error::Refused {
                key: self.key.clone(),
                said: "the runtime signed nothing".to_string(),
            })?;
        let mut request = signed_request(&target, "PUT");
        for (name, value) in [
            ("content-type", &self.options.content_type),
            ("cache-control", &self.options.cache_control),
            ("if-none-match", &self.options.if_none_match),
            ("if-match", &self.options.if_match),
        ] {
            if !value.is_empty() {
                request = request.header(name, value);
            }
        }
        let body = Bytes::from(std::mem::take(&mut self.buffered));
        let response = send(&self.reached, request, body).await?;
        refusal(&self.key, response.status().as_u16())
    }

    async fn abort(&mut self) {
        let upload_id = std::mem::take(&mut *self.leash.held());
        if upload_id.is_empty() {
            return;
        }
        let _ = self
            .reached
            .client
            .abort_multipart(AbortMultipartRequest {
                bucket: self.reached.bucket.clone(),
                key: self.key.clone(),
                upload_id,
                ..Default::default()
            })
            .await;
    }
}

fn drive(working: &mut Working, context: &mut Context<'_>) -> Poll<(Box<Core>, Option<Error>)> {
    let (core, outcome) = std::task::ready!(working.as_mut().poll(context));
    Poll::Ready((core, outcome.err()))
}

impl Writer {
    fn poll_drain(&mut self, context: &mut Context<'_>) -> Poll<std::io::Result<()>> {
        let State::Draining(working) = &mut self.state else {
            return Poll::Ready(Ok(()));
        };
        let (core, failure) = std::task::ready!(drive(working, context));
        match failure {
            None => {
                self.state = State::Ready(core);
                Poll::Ready(Ok(()))
            }
            Some(err) => {
                self.state = State::Spent;
                Poll::Ready(Err(std::io::Error::other(err.to_string())))
            }
        }
    }
}

impl tokio::io::AsyncWrite for Writer {
    fn poll_write(
        mut self: Pin<&mut Self>,
        context: &mut Context<'_>,
        buffer: &[u8],
    ) -> Poll<std::io::Result<usize>> {
        std::task::ready!(self.poll_drain(context))?;
        let State::Ready(core) = &mut self.state else {
            return Poll::Ready(Err(std::io::Error::other(
                "this writer has already been settled",
            )));
        };
        core.buffered.extend_from_slice(buffer);
        if core.buffered.len() > core.thresholds.single_ceiling {
            let State::Ready(mut core) = std::mem::replace(&mut self.state, State::Spent) else {
                unreachable!("the state was just read as ready")
            };
            self.state = State::Draining(Box::pin(async move {
                let outcome = core.drain(false).await;
                if outcome.is_err() {
                    core.abort().await;
                }
                (core, outcome.map(|()| None))
            }));
        }
        Poll::Ready(Ok(buffer.len()))
    }

    fn poll_flush(
        mut self: Pin<&mut Self>,
        context: &mut Context<'_>,
    ) -> Poll<std::io::Result<()>> {
        self.poll_drain(context)
    }

    fn poll_shutdown(
        mut self: Pin<&mut Self>,
        context: &mut Context<'_>,
    ) -> Poll<std::io::Result<()>> {
        if matches!(self.state, State::Spent) {
            return Poll::Ready(Ok(()));
        }
        if !matches!(self.state, State::Finishing(_)) {
            std::task::ready!(self.poll_drain(context))?;
            let State::Ready(mut core) = std::mem::replace(&mut self.state, State::Spent) else {
                return Poll::Ready(Err(std::io::Error::other(
                    "this writer has already been settled",
                )));
            };
            self.state = State::Finishing(Box::pin(async move {
                let outcome = core.finish().await;
                if outcome.is_err() {
                    core.abort().await;
                }
                (core, outcome)
            }));
        }
        let State::Finishing(working) = &mut self.state else {
            unreachable!("the state was just set to finishing")
        };
        let (_, failure) = std::task::ready!(drive(working, context));
        self.state = State::Spent;
        match failure {
            None => Poll::Ready(Ok(())),
            Some(err) => Poll::Ready(Err(std::io::Error::other(err.to_string()))),
        }
    }
}

impl Drop for Writer {
    fn drop(&mut self) {
        let upload_id = std::mem::take(&mut *self.leash.held());
        if upload_id.is_empty() {
            return;
        }
        let Ok(handle) = tokio::runtime::Handle::try_current() else {
            return;
        };
        let reached = self.leash.reached.clone();
        let request = AbortMultipartRequest {
            bucket: reached.bucket.clone(),
            key: self.leash.key.clone(),
            upload_id,
            ..Default::default()
        };
        handle.spawn(async move {
            let _ = reached.client.abort_multipart(request).await;
        });
    }
}
