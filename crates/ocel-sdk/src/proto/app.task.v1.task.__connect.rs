///Shorthand for `OwnedView<TriggerRequestView<'static>>`.
pub type OwnedTriggerRequestView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::TriggerRequestView<'static>,
>;
///Shorthand for `OwnedView<TriggerResponseView<'static>>`.
pub type OwnedTriggerResponseView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::TriggerResponseView<'static>,
>;
///Shorthand for `OwnedView<BatchTriggerRequestView<'static>>`.
pub type OwnedBatchTriggerRequestView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::BatchTriggerRequestView<'static>,
>;
///Shorthand for `OwnedView<BatchTriggerResponseView<'static>>`.
pub type OwnedBatchTriggerResponseView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::BatchTriggerResponseView<'static>,
>;
///Shorthand for `OwnedView<RetrieveRunRequestView<'static>>`.
pub type OwnedRetrieveRunRequestView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::RetrieveRunRequestView<'static>,
>;
///Shorthand for `OwnedView<RetrieveRunResponseView<'static>>`.
pub type OwnedRetrieveRunResponseView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::RetrieveRunResponseView<'static>,
>;
///Shorthand for `OwnedView<ListRunsRequestView<'static>>`.
pub type OwnedListRunsRequestView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::ListRunsRequestView<'static>,
>;
///Shorthand for `OwnedView<ListRunsResponseView<'static>>`.
pub type OwnedListRunsResponseView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::ListRunsResponseView<'static>,
>;
///Shorthand for `OwnedView<CancelRunRequestView<'static>>`.
pub type OwnedCancelRunRequestView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::CancelRunRequestView<'static>,
>;
///Shorthand for `OwnedView<CancelRunResponseView<'static>>`.
pub type OwnedCancelRunResponseView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::CancelRunResponseView<'static>,
>;
///Shorthand for `OwnedView<ReplayRunRequestView<'static>>`.
pub type OwnedReplayRunRequestView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::ReplayRunRequestView<'static>,
>;
///Shorthand for `OwnedView<ReplayRunResponseView<'static>>`.
pub type OwnedReplayRunResponseView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::ReplayRunResponseView<'static>,
>;
///Shorthand for `OwnedView<RescheduleRunRequestView<'static>>`.
pub type OwnedRescheduleRunRequestView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::RescheduleRunRequestView<'static>,
>;
///Shorthand for `OwnedView<RescheduleRunResponseView<'static>>`.
pub type OwnedRescheduleRunResponseView = ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::RescheduleRunResponseView<'static>,
>;
impl ::connectrpc::Encodable<crate::proto::app::task::v1::TriggerResponse>
for crate::proto::app::task::v1::__buffa::view::TriggerResponseView<'_> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self, codec)
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::TriggerResponse>
for ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::TriggerResponseView<'static>,
> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self.reborrow(), codec)
    }
    /// An `OwnedView` still holds the buffer it was decoded from, so
    /// its large fields can be handed to the response body by
    /// reference count instead of copied. The bare view impl above
    /// cannot do this: it has borrows but no buffer to name.
    fn encode_segments(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::connectrpc::EncodedBody, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body_segments(
            self.reborrow(),
            self.bytes(),
            codec,
        )
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::BatchTriggerResponse>
for crate::proto::app::task::v1::__buffa::view::BatchTriggerResponseView<'_> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self, codec)
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::BatchTriggerResponse>
for ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::BatchTriggerResponseView<'static>,
> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self.reborrow(), codec)
    }
    /// An `OwnedView` still holds the buffer it was decoded from, so
    /// its large fields can be handed to the response body by
    /// reference count instead of copied. The bare view impl above
    /// cannot do this: it has borrows but no buffer to name.
    fn encode_segments(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::connectrpc::EncodedBody, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body_segments(
            self.reborrow(),
            self.bytes(),
            codec,
        )
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::RetrieveRunResponse>
for crate::proto::app::task::v1::__buffa::view::RetrieveRunResponseView<'_> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self, codec)
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::RetrieveRunResponse>
for ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::RetrieveRunResponseView<'static>,
> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self.reborrow(), codec)
    }
    /// An `OwnedView` still holds the buffer it was decoded from, so
    /// its large fields can be handed to the response body by
    /// reference count instead of copied. The bare view impl above
    /// cannot do this: it has borrows but no buffer to name.
    fn encode_segments(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::connectrpc::EncodedBody, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body_segments(
            self.reborrow(),
            self.bytes(),
            codec,
        )
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::ListRunsResponse>
for crate::proto::app::task::v1::__buffa::view::ListRunsResponseView<'_> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self, codec)
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::ListRunsResponse>
for ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::ListRunsResponseView<'static>,
> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self.reborrow(), codec)
    }
    /// An `OwnedView` still holds the buffer it was decoded from, so
    /// its large fields can be handed to the response body by
    /// reference count instead of copied. The bare view impl above
    /// cannot do this: it has borrows but no buffer to name.
    fn encode_segments(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::connectrpc::EncodedBody, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body_segments(
            self.reborrow(),
            self.bytes(),
            codec,
        )
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::CancelRunResponse>
for crate::proto::app::task::v1::__buffa::view::CancelRunResponseView<'_> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self, codec)
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::CancelRunResponse>
for ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::CancelRunResponseView<'static>,
> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self.reborrow(), codec)
    }
    /// An `OwnedView` still holds the buffer it was decoded from, so
    /// its large fields can be handed to the response body by
    /// reference count instead of copied. The bare view impl above
    /// cannot do this: it has borrows but no buffer to name.
    fn encode_segments(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::connectrpc::EncodedBody, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body_segments(
            self.reborrow(),
            self.bytes(),
            codec,
        )
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::ReplayRunResponse>
for crate::proto::app::task::v1::__buffa::view::ReplayRunResponseView<'_> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self, codec)
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::ReplayRunResponse>
for ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::ReplayRunResponseView<'static>,
> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self.reborrow(), codec)
    }
    /// An `OwnedView` still holds the buffer it was decoded from, so
    /// its large fields can be handed to the response body by
    /// reference count instead of copied. The bare view impl above
    /// cannot do this: it has borrows but no buffer to name.
    fn encode_segments(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::connectrpc::EncodedBody, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body_segments(
            self.reborrow(),
            self.bytes(),
            codec,
        )
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::RescheduleRunResponse>
for crate::proto::app::task::v1::__buffa::view::RescheduleRunResponseView<'_> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self, codec)
    }
}
impl ::connectrpc::Encodable<crate::proto::app::task::v1::RescheduleRunResponse>
for ::buffa::view::OwnedView<
    crate::proto::app::task::v1::__buffa::view::RescheduleRunResponseView<'static>,
> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self.reborrow(), codec)
    }
    /// An `OwnedView` still holds the buffer it was decoded from, so
    /// its large fields can be handed to the response body by
    /// reference count instead of copied. The bare view impl above
    /// cannot do this: it has borrows but no buffer to name.
    fn encode_segments(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::connectrpc::EncodedBody, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body_segments(
            self.reborrow(),
            self.bytes(),
            codec,
        )
    }
}
/// Full service name for this service.
pub const TASK_SERVICE_SERVICE_NAME: &str = "app.task.v1.TaskService";
/// Static [`Spec`](::connectrpc::Spec) for the `Trigger` RPC, as seen by the server; the generated client passes it with [`origin`](::connectrpc::Spec::origin) `Client` (compare across sides with [`Spec::same_method`](::connectrpc::Spec::same_method)).
pub const TASK_SERVICE_TRIGGER_SPEC: ::connectrpc::Spec = ::connectrpc::Spec::server(
        "/app.task.v1.TaskService/Trigger",
        ::connectrpc::StreamType::Unary,
    )
    .with_idempotency_level(::connectrpc::IdempotencyLevel::Unknown);
/// Static [`Spec`](::connectrpc::Spec) for the `BatchTrigger` RPC, as seen by the server; the generated client passes it with [`origin`](::connectrpc::Spec::origin) `Client` (compare across sides with [`Spec::same_method`](::connectrpc::Spec::same_method)).
pub const TASK_SERVICE_BATCH_TRIGGER_SPEC: ::connectrpc::Spec = ::connectrpc::Spec::server(
        "/app.task.v1.TaskService/BatchTrigger",
        ::connectrpc::StreamType::Unary,
    )
    .with_idempotency_level(::connectrpc::IdempotencyLevel::Unknown);
/// Static [`Spec`](::connectrpc::Spec) for the `RetrieveRun` RPC, as seen by the server; the generated client passes it with [`origin`](::connectrpc::Spec::origin) `Client` (compare across sides with [`Spec::same_method`](::connectrpc::Spec::same_method)).
pub const TASK_SERVICE_RETRIEVE_RUN_SPEC: ::connectrpc::Spec = ::connectrpc::Spec::server(
        "/app.task.v1.TaskService/RetrieveRun",
        ::connectrpc::StreamType::Unary,
    )
    .with_idempotency_level(::connectrpc::IdempotencyLevel::Unknown);
/// Static [`Spec`](::connectrpc::Spec) for the `ListRuns` RPC, as seen by the server; the generated client passes it with [`origin`](::connectrpc::Spec::origin) `Client` (compare across sides with [`Spec::same_method`](::connectrpc::Spec::same_method)).
pub const TASK_SERVICE_LIST_RUNS_SPEC: ::connectrpc::Spec = ::connectrpc::Spec::server(
        "/app.task.v1.TaskService/ListRuns",
        ::connectrpc::StreamType::Unary,
    )
    .with_idempotency_level(::connectrpc::IdempotencyLevel::Unknown);
/// Static [`Spec`](::connectrpc::Spec) for the `CancelRun` RPC, as seen by the server; the generated client passes it with [`origin`](::connectrpc::Spec::origin) `Client` (compare across sides with [`Spec::same_method`](::connectrpc::Spec::same_method)).
pub const TASK_SERVICE_CANCEL_RUN_SPEC: ::connectrpc::Spec = ::connectrpc::Spec::server(
        "/app.task.v1.TaskService/CancelRun",
        ::connectrpc::StreamType::Unary,
    )
    .with_idempotency_level(::connectrpc::IdempotencyLevel::Unknown);
/// Static [`Spec`](::connectrpc::Spec) for the `ReplayRun` RPC, as seen by the server; the generated client passes it with [`origin`](::connectrpc::Spec::origin) `Client` (compare across sides with [`Spec::same_method`](::connectrpc::Spec::same_method)).
pub const TASK_SERVICE_REPLAY_RUN_SPEC: ::connectrpc::Spec = ::connectrpc::Spec::server(
        "/app.task.v1.TaskService/ReplayRun",
        ::connectrpc::StreamType::Unary,
    )
    .with_idempotency_level(::connectrpc::IdempotencyLevel::Unknown);
/// Static [`Spec`](::connectrpc::Spec) for the `RescheduleRun` RPC, as seen by the server; the generated client passes it with [`origin`](::connectrpc::Spec::origin) `Client` (compare across sides with [`Spec::same_method`](::connectrpc::Spec::same_method)).
pub const TASK_SERVICE_RESCHEDULE_RUN_SPEC: ::connectrpc::Spec = ::connectrpc::Spec::server(
        "/app.task.v1.TaskService/RescheduleRun",
        ::connectrpc::StreamType::Unary,
    )
    .with_idempotency_level(::connectrpc::IdempotencyLevel::Unknown);
/// Server trait for TaskService.
///
/// # Implementing handlers
///
/// Implement methods with plain `async fn`; the returned future satisfies
/// the `Send` bound automatically.
///
/// **Unary and server-streaming requests** arrive as
/// [`ServiceRequest<'_, Req>`](::connectrpc::ServiceRequest): a zero-copy
/// view of the request plus its body, valid for the duration of the call.
/// Fields are read directly (`request.name` is a `&str` into the decoded
/// buffer) and the borrow may be held across `.await` points. Anything
/// that must outlive the call — `tokio::spawn`, channels, server state,
/// or data captured by a returned response stream — takes owned data:
/// call `request.to_owned_message()` (or copy the specific fields)
/// first.
///
/// **Client-streaming and bidi requests** arrive as
/// [`InboundStream<Req>`](::connectrpc::InboundStream) — a
/// `ServiceStream` of [`StreamMessage`](::connectrpc::StreamMessage)s.
/// Each item owns its decoded buffer and is `Send + 'static`, so items
/// can be buffered or moved into spawned tasks; read fields zero-copy
/// through the generated accessor methods (`item.name()`) or `.view()`,
/// convert with `.to_owned_message()`, or yield an item back unchanged —
/// `StreamMessage<M>` implements `Encodable<M>`.
///
/// Request types resolved through `extern_path` (e.g. well-known types
/// from another crate) use the same wrappers; the crate that owns the
/// type must be generated with buffa ≥ 0.9.0 and views enabled so the
/// backing `HasMessageView` impl exists.
///
/// The `impl Encodable<Out>` return bound accepts the owned `Out`, the
/// generated `OutView<'_>` / `OwnedOutView`,
/// [`MaybeBorrowed`](::connectrpc::MaybeBorrowed), or
/// [`PreEncoded`](::connectrpc::PreEncoded) for handlers that encode a
/// non-`'static` view internally and pass the bytes across the handler
/// boundary. View bodies are not emitted for output types mapped via
/// `extern_path` (the impl would be an orphan); return owned for
/// WKT/extern outputs.
///
/// Server-streaming and bidi-streaming methods return
/// `ServiceStream<impl Encodable<Out> + Send + use<Self>>`. The
/// `use<Self>` precise-capturing clause excludes `&self`'s lifetime and
/// the request's lifetime (unary methods use `use<'a, Self>` and may
/// borrow from `&self`), so stream items must be `'static` and cannot
/// borrow from the request. To stream view-encoded data, encode each
/// item inside the stream body and yield
/// [`PreEncoded`](::connectrpc::PreEncoded) — see its `# Streaming
/// example` doc.
#[allow(clippy::type_complexity)]
pub trait TaskService: Send + Sync + 'static {
    /// Handle the Trigger RPC.
    ///
    /// `'a` lets the response body borrow from `&self` (e.g. server-resident state).
    ///
    /// `request` is borrowed from the request body and is valid for the
    /// duration of the call; message fields are read directly on it
    /// (zero-copy). The response cannot borrow from `request` — use
    /// `.to_owned_message()` (or copy the specific fields) for anything
    /// returned, stored, or moved into `tokio::spawn`.
    fn trigger<'a>(
        &'a self,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::ServiceRequest<
            '_,
            crate::proto::app::task::v1::TriggerRequest,
        >,
    ) -> impl ::std::future::Future<
        Output = ::connectrpc::ServiceResult<
            impl ::connectrpc::Encodable<
                crate::proto::app::task::v1::TriggerResponse,
            > + Send + use<'a, Self>,
        >,
    > + Send;
    /// Handle the BatchTrigger RPC.
    ///
    /// `'a` lets the response body borrow from `&self` (e.g. server-resident state).
    ///
    /// `request` is borrowed from the request body and is valid for the
    /// duration of the call; message fields are read directly on it
    /// (zero-copy). The response cannot borrow from `request` — use
    /// `.to_owned_message()` (or copy the specific fields) for anything
    /// returned, stored, or moved into `tokio::spawn`.
    fn batch_trigger<'a>(
        &'a self,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::ServiceRequest<
            '_,
            crate::proto::app::task::v1::BatchTriggerRequest,
        >,
    ) -> impl ::std::future::Future<
        Output = ::connectrpc::ServiceResult<
            impl ::connectrpc::Encodable<
                crate::proto::app::task::v1::BatchTriggerResponse,
            > + Send + use<'a, Self>,
        >,
    > + Send;
    /// Handle the RetrieveRun RPC.
    ///
    /// `'a` lets the response body borrow from `&self` (e.g. server-resident state).
    ///
    /// `request` is borrowed from the request body and is valid for the
    /// duration of the call; message fields are read directly on it
    /// (zero-copy). The response cannot borrow from `request` — use
    /// `.to_owned_message()` (or copy the specific fields) for anything
    /// returned, stored, or moved into `tokio::spawn`.
    fn retrieve_run<'a>(
        &'a self,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::ServiceRequest<
            '_,
            crate::proto::app::task::v1::RetrieveRunRequest,
        >,
    ) -> impl ::std::future::Future<
        Output = ::connectrpc::ServiceResult<
            impl ::connectrpc::Encodable<
                crate::proto::app::task::v1::RetrieveRunResponse,
            > + Send + use<'a, Self>,
        >,
    > + Send;
    /// Handle the ListRuns RPC.
    ///
    /// `'a` lets the response body borrow from `&self` (e.g. server-resident state).
    ///
    /// `request` is borrowed from the request body and is valid for the
    /// duration of the call; message fields are read directly on it
    /// (zero-copy). The response cannot borrow from `request` — use
    /// `.to_owned_message()` (or copy the specific fields) for anything
    /// returned, stored, or moved into `tokio::spawn`.
    fn list_runs<'a>(
        &'a self,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::ServiceRequest<
            '_,
            crate::proto::app::task::v1::ListRunsRequest,
        >,
    ) -> impl ::std::future::Future<
        Output = ::connectrpc::ServiceResult<
            impl ::connectrpc::Encodable<
                crate::proto::app::task::v1::ListRunsResponse,
            > + Send + use<'a, Self>,
        >,
    > + Send;
    /// Handle the CancelRun RPC.
    ///
    /// `'a` lets the response body borrow from `&self` (e.g. server-resident state).
    ///
    /// `request` is borrowed from the request body and is valid for the
    /// duration of the call; message fields are read directly on it
    /// (zero-copy). The response cannot borrow from `request` — use
    /// `.to_owned_message()` (or copy the specific fields) for anything
    /// returned, stored, or moved into `tokio::spawn`.
    fn cancel_run<'a>(
        &'a self,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::ServiceRequest<
            '_,
            crate::proto::app::task::v1::CancelRunRequest,
        >,
    ) -> impl ::std::future::Future<
        Output = ::connectrpc::ServiceResult<
            impl ::connectrpc::Encodable<
                crate::proto::app::task::v1::CancelRunResponse,
            > + Send + use<'a, Self>,
        >,
    > + Send;
    /// Handle the ReplayRun RPC.
    ///
    /// `'a` lets the response body borrow from `&self` (e.g. server-resident state).
    ///
    /// `request` is borrowed from the request body and is valid for the
    /// duration of the call; message fields are read directly on it
    /// (zero-copy). The response cannot borrow from `request` — use
    /// `.to_owned_message()` (or copy the specific fields) for anything
    /// returned, stored, or moved into `tokio::spawn`.
    fn replay_run<'a>(
        &'a self,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::ServiceRequest<
            '_,
            crate::proto::app::task::v1::ReplayRunRequest,
        >,
    ) -> impl ::std::future::Future<
        Output = ::connectrpc::ServiceResult<
            impl ::connectrpc::Encodable<
                crate::proto::app::task::v1::ReplayRunResponse,
            > + Send + use<'a, Self>,
        >,
    > + Send;
    /// Handle the RescheduleRun RPC.
    ///
    /// `'a` lets the response body borrow from `&self` (e.g. server-resident state).
    ///
    /// `request` is borrowed from the request body and is valid for the
    /// duration of the call; message fields are read directly on it
    /// (zero-copy). The response cannot borrow from `request` — use
    /// `.to_owned_message()` (or copy the specific fields) for anything
    /// returned, stored, or moved into `tokio::spawn`.
    fn reschedule_run<'a>(
        &'a self,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::ServiceRequest<
            '_,
            crate::proto::app::task::v1::RescheduleRunRequest,
        >,
    ) -> impl ::std::future::Future<
        Output = ::connectrpc::ServiceResult<
            impl ::connectrpc::Encodable<
                crate::proto::app::task::v1::RescheduleRunResponse,
            > + Send + use<'a, Self>,
        >,
    > + Send;
}
/// Extension trait for registering a service implementation with a Router.
///
/// This trait is automatically implemented for all types that implement the service trait.
/// Prefer [`Router::add_service`](::connectrpc::Router::add_service) for
/// top-down registration; `register` remains available for compatibility
/// and cases where the service-first call shape is more convenient.
///
/// # Example
///
/// ```rust,ignore
/// use std::sync::Arc;
///
/// let service = Arc::new(MyServiceImpl);
/// let router = service.register(Router::new());
/// ```
pub trait TaskServiceExt: TaskService {
    /// Register this service implementation with a Router.
    ///
    /// Takes ownership of the `Arc<Self>` and returns a new Router with
    /// this service's methods registered.
    fn register(
        self: ::std::sync::Arc<Self>,
        router: ::connectrpc::Router,
    ) -> ::connectrpc::Router;
}
impl<S: TaskService> TaskServiceExt for S {
    fn register(
        self: ::std::sync::Arc<Self>,
        router: ::connectrpc::Router,
    ) -> ::connectrpc::Router {
        router
            .route_view(
                TASK_SERVICE_SERVICE_NAME,
                "Trigger",
                {
                    let svc = ::std::sync::Arc::clone(&self);
                    ::connectrpc::view_handler_fn(move |
                        ctx,
                        req: ::buffa::view::OwnedView<
                            crate::proto::app::task::v1::__buffa::view::TriggerRequestView<
                                'static,
                            >,
                        >,
                        format|
                    {
                        let svc = ::std::sync::Arc::clone(&svc);
                        async move {
                            let sreq = ::connectrpc::ServiceRequest::<
                                crate::proto::app::task::v1::TriggerRequest,
                            >::from_parts(req.reborrow(), req.bytes());
                            svc.trigger(ctx, sreq)
                                .await?
                                .encode::<
                                    crate::proto::app::task::v1::TriggerResponse,
                                >(format)
                        }
                    })
                },
            )
            .with_spec(TASK_SERVICE_TRIGGER_SPEC)
            .route_view(
                TASK_SERVICE_SERVICE_NAME,
                "BatchTrigger",
                {
                    let svc = ::std::sync::Arc::clone(&self);
                    ::connectrpc::view_handler_fn(move |
                        ctx,
                        req: ::buffa::view::OwnedView<
                            crate::proto::app::task::v1::__buffa::view::BatchTriggerRequestView<
                                'static,
                            >,
                        >,
                        format|
                    {
                        let svc = ::std::sync::Arc::clone(&svc);
                        async move {
                            let sreq = ::connectrpc::ServiceRequest::<
                                crate::proto::app::task::v1::BatchTriggerRequest,
                            >::from_parts(req.reborrow(), req.bytes());
                            svc.batch_trigger(ctx, sreq)
                                .await?
                                .encode::<
                                    crate::proto::app::task::v1::BatchTriggerResponse,
                                >(format)
                        }
                    })
                },
            )
            .with_spec(TASK_SERVICE_BATCH_TRIGGER_SPEC)
            .route_view(
                TASK_SERVICE_SERVICE_NAME,
                "RetrieveRun",
                {
                    let svc = ::std::sync::Arc::clone(&self);
                    ::connectrpc::view_handler_fn(move |
                        ctx,
                        req: ::buffa::view::OwnedView<
                            crate::proto::app::task::v1::__buffa::view::RetrieveRunRequestView<
                                'static,
                            >,
                        >,
                        format|
                    {
                        let svc = ::std::sync::Arc::clone(&svc);
                        async move {
                            let sreq = ::connectrpc::ServiceRequest::<
                                crate::proto::app::task::v1::RetrieveRunRequest,
                            >::from_parts(req.reborrow(), req.bytes());
                            svc.retrieve_run(ctx, sreq)
                                .await?
                                .encode::<
                                    crate::proto::app::task::v1::RetrieveRunResponse,
                                >(format)
                        }
                    })
                },
            )
            .with_spec(TASK_SERVICE_RETRIEVE_RUN_SPEC)
            .route_view(
                TASK_SERVICE_SERVICE_NAME,
                "ListRuns",
                {
                    let svc = ::std::sync::Arc::clone(&self);
                    ::connectrpc::view_handler_fn(move |
                        ctx,
                        req: ::buffa::view::OwnedView<
                            crate::proto::app::task::v1::__buffa::view::ListRunsRequestView<
                                'static,
                            >,
                        >,
                        format|
                    {
                        let svc = ::std::sync::Arc::clone(&svc);
                        async move {
                            let sreq = ::connectrpc::ServiceRequest::<
                                crate::proto::app::task::v1::ListRunsRequest,
                            >::from_parts(req.reborrow(), req.bytes());
                            svc.list_runs(ctx, sreq)
                                .await?
                                .encode::<
                                    crate::proto::app::task::v1::ListRunsResponse,
                                >(format)
                        }
                    })
                },
            )
            .with_spec(TASK_SERVICE_LIST_RUNS_SPEC)
            .route_view(
                TASK_SERVICE_SERVICE_NAME,
                "CancelRun",
                {
                    let svc = ::std::sync::Arc::clone(&self);
                    ::connectrpc::view_handler_fn(move |
                        ctx,
                        req: ::buffa::view::OwnedView<
                            crate::proto::app::task::v1::__buffa::view::CancelRunRequestView<
                                'static,
                            >,
                        >,
                        format|
                    {
                        let svc = ::std::sync::Arc::clone(&svc);
                        async move {
                            let sreq = ::connectrpc::ServiceRequest::<
                                crate::proto::app::task::v1::CancelRunRequest,
                            >::from_parts(req.reborrow(), req.bytes());
                            svc.cancel_run(ctx, sreq)
                                .await?
                                .encode::<
                                    crate::proto::app::task::v1::CancelRunResponse,
                                >(format)
                        }
                    })
                },
            )
            .with_spec(TASK_SERVICE_CANCEL_RUN_SPEC)
            .route_view(
                TASK_SERVICE_SERVICE_NAME,
                "ReplayRun",
                {
                    let svc = ::std::sync::Arc::clone(&self);
                    ::connectrpc::view_handler_fn(move |
                        ctx,
                        req: ::buffa::view::OwnedView<
                            crate::proto::app::task::v1::__buffa::view::ReplayRunRequestView<
                                'static,
                            >,
                        >,
                        format|
                    {
                        let svc = ::std::sync::Arc::clone(&svc);
                        async move {
                            let sreq = ::connectrpc::ServiceRequest::<
                                crate::proto::app::task::v1::ReplayRunRequest,
                            >::from_parts(req.reborrow(), req.bytes());
                            svc.replay_run(ctx, sreq)
                                .await?
                                .encode::<
                                    crate::proto::app::task::v1::ReplayRunResponse,
                                >(format)
                        }
                    })
                },
            )
            .with_spec(TASK_SERVICE_REPLAY_RUN_SPEC)
            .route_view(
                TASK_SERVICE_SERVICE_NAME,
                "RescheduleRun",
                {
                    let svc = ::std::sync::Arc::clone(&self);
                    ::connectrpc::view_handler_fn(move |
                        ctx,
                        req: ::buffa::view::OwnedView<
                            crate::proto::app::task::v1::__buffa::view::RescheduleRunRequestView<
                                'static,
                            >,
                        >,
                        format|
                    {
                        let svc = ::std::sync::Arc::clone(&svc);
                        async move {
                            let sreq = ::connectrpc::ServiceRequest::<
                                crate::proto::app::task::v1::RescheduleRunRequest,
                            >::from_parts(req.reborrow(), req.bytes());
                            svc.reschedule_run(ctx, sreq)
                                .await?
                                .encode::<
                                    crate::proto::app::task::v1::RescheduleRunResponse,
                                >(format)
                        }
                    })
                },
            )
            .with_spec(TASK_SERVICE_RESCHEDULE_RUN_SPEC)
    }
}
/// Type-inference marker used by [`Router::add_service`](::connectrpc::Router::add_service).
#[doc(hidden)]
pub struct TaskServiceRegisterMarker;
impl<S: TaskService> ::connectrpc::ServiceRegister<TaskServiceRegisterMarker>
for ::std::sync::Arc<S> {
    fn register_service(self, router: ::connectrpc::Router) -> ::connectrpc::Router {
        <S as TaskServiceExt>::register(self, router)
    }
}
/// Monomorphic dispatcher for `TaskService`.
///
/// Unlike `.register(Router)` which type-erases each method into an `Arc<dyn ErasedHandler>` stored in a `HashMap`, this struct dispatches via a compile-time `match` on method name: no vtable, no hash lookup.
///
/// # Example
///
/// ```rust,ignore
/// use connectrpc::ConnectRpcService;
///
/// let server = TaskServiceServer::new(MyImpl);
/// let service = ConnectRpcService::new(server);
/// // hand `service` to axum/hyper as a fallback_service
/// ```
pub struct TaskServiceServer<T> {
    inner: ::std::sync::Arc<T>,
}
impl<T: TaskService> TaskServiceServer<T> {
    /// Wrap a service implementation in a monomorphic dispatcher.
    pub fn new(service: T) -> Self {
        Self {
            inner: ::std::sync::Arc::new(service),
        }
    }
    /// Wrap an already-`Arc`'d service implementation.
    pub fn from_arc(inner: ::std::sync::Arc<T>) -> Self {
        Self { inner }
    }
}
impl<T> Clone for TaskServiceServer<T> {
    fn clone(&self) -> Self {
        Self {
            inner: ::std::sync::Arc::clone(&self.inner),
        }
    }
}
impl<T: TaskService> ::connectrpc::Dispatcher for TaskServiceServer<T> {
    #[inline]
    fn lookup(
        &self,
        path: &str,
    ) -> Option<::connectrpc::dispatcher::codegen::MethodDescriptor> {
        let method = path.strip_prefix("app.task.v1.TaskService/")?;
        match method {
            "Trigger" => {
                Some(
                    ::connectrpc::dispatcher::codegen::MethodDescriptor::unary(false)
                        .with_spec(TASK_SERVICE_TRIGGER_SPEC),
                )
            }
            "BatchTrigger" => {
                Some(
                    ::connectrpc::dispatcher::codegen::MethodDescriptor::unary(false)
                        .with_spec(TASK_SERVICE_BATCH_TRIGGER_SPEC),
                )
            }
            "RetrieveRun" => {
                Some(
                    ::connectrpc::dispatcher::codegen::MethodDescriptor::unary(false)
                        .with_spec(TASK_SERVICE_RETRIEVE_RUN_SPEC),
                )
            }
            "ListRuns" => {
                Some(
                    ::connectrpc::dispatcher::codegen::MethodDescriptor::unary(false)
                        .with_spec(TASK_SERVICE_LIST_RUNS_SPEC),
                )
            }
            "CancelRun" => {
                Some(
                    ::connectrpc::dispatcher::codegen::MethodDescriptor::unary(false)
                        .with_spec(TASK_SERVICE_CANCEL_RUN_SPEC),
                )
            }
            "ReplayRun" => {
                Some(
                    ::connectrpc::dispatcher::codegen::MethodDescriptor::unary(false)
                        .with_spec(TASK_SERVICE_REPLAY_RUN_SPEC),
                )
            }
            "RescheduleRun" => {
                Some(
                    ::connectrpc::dispatcher::codegen::MethodDescriptor::unary(false)
                        .with_spec(TASK_SERVICE_RESCHEDULE_RUN_SPEC),
                )
            }
            _ => None,
        }
    }
    fn call_unary(
        &self,
        path: &str,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::Payload,
        format: ::connectrpc::CodecFormat,
    ) -> ::connectrpc::dispatcher::codegen::UnaryResult {
        let Some(method) = path.strip_prefix("app.task.v1.TaskService/") else {
            return ::connectrpc::dispatcher::codegen::unimplemented_unary(path);
        };
        let _ = (&ctx, &request, &format);
        match method {
            "Trigger" => {
                let svc = ::std::sync::Arc::clone(&self.inner);
                Box::pin(async move {
                    let body = ::connectrpc::dispatcher::codegen::request_proto_bytes::<
                        crate::proto::app::task::v1::TriggerRequest,
                    >(request.encoded()?, format)?;
                    let req: crate::proto::app::task::v1::__buffa::view::TriggerRequestView<
                        '_,
                    > = ::connectrpc::dispatcher::codegen::decode_borrowed_request_view(
                        &body,
                        ctx.decode_options(),
                    )?;
                    let req = ::connectrpc::ServiceRequest::<
                        crate::proto::app::task::v1::TriggerRequest,
                    >::from_parts(&req, &body);
                    svc.trigger(ctx, req)
                        .await?
                        .encode::<crate::proto::app::task::v1::TriggerResponse>(format)
                })
            }
            "BatchTrigger" => {
                let svc = ::std::sync::Arc::clone(&self.inner);
                Box::pin(async move {
                    let body = ::connectrpc::dispatcher::codegen::request_proto_bytes::<
                        crate::proto::app::task::v1::BatchTriggerRequest,
                    >(request.encoded()?, format)?;
                    let req: crate::proto::app::task::v1::__buffa::view::BatchTriggerRequestView<
                        '_,
                    > = ::connectrpc::dispatcher::codegen::decode_borrowed_request_view(
                        &body,
                        ctx.decode_options(),
                    )?;
                    let req = ::connectrpc::ServiceRequest::<
                        crate::proto::app::task::v1::BatchTriggerRequest,
                    >::from_parts(&req, &body);
                    svc.batch_trigger(ctx, req)
                        .await?
                        .encode::<
                            crate::proto::app::task::v1::BatchTriggerResponse,
                        >(format)
                })
            }
            "RetrieveRun" => {
                let svc = ::std::sync::Arc::clone(&self.inner);
                Box::pin(async move {
                    let body = ::connectrpc::dispatcher::codegen::request_proto_bytes::<
                        crate::proto::app::task::v1::RetrieveRunRequest,
                    >(request.encoded()?, format)?;
                    let req: crate::proto::app::task::v1::__buffa::view::RetrieveRunRequestView<
                        '_,
                    > = ::connectrpc::dispatcher::codegen::decode_borrowed_request_view(
                        &body,
                        ctx.decode_options(),
                    )?;
                    let req = ::connectrpc::ServiceRequest::<
                        crate::proto::app::task::v1::RetrieveRunRequest,
                    >::from_parts(&req, &body);
                    svc.retrieve_run(ctx, req)
                        .await?
                        .encode::<
                            crate::proto::app::task::v1::RetrieveRunResponse,
                        >(format)
                })
            }
            "ListRuns" => {
                let svc = ::std::sync::Arc::clone(&self.inner);
                Box::pin(async move {
                    let body = ::connectrpc::dispatcher::codegen::request_proto_bytes::<
                        crate::proto::app::task::v1::ListRunsRequest,
                    >(request.encoded()?, format)?;
                    let req: crate::proto::app::task::v1::__buffa::view::ListRunsRequestView<
                        '_,
                    > = ::connectrpc::dispatcher::codegen::decode_borrowed_request_view(
                        &body,
                        ctx.decode_options(),
                    )?;
                    let req = ::connectrpc::ServiceRequest::<
                        crate::proto::app::task::v1::ListRunsRequest,
                    >::from_parts(&req, &body);
                    svc.list_runs(ctx, req)
                        .await?
                        .encode::<crate::proto::app::task::v1::ListRunsResponse>(format)
                })
            }
            "CancelRun" => {
                let svc = ::std::sync::Arc::clone(&self.inner);
                Box::pin(async move {
                    let body = ::connectrpc::dispatcher::codegen::request_proto_bytes::<
                        crate::proto::app::task::v1::CancelRunRequest,
                    >(request.encoded()?, format)?;
                    let req: crate::proto::app::task::v1::__buffa::view::CancelRunRequestView<
                        '_,
                    > = ::connectrpc::dispatcher::codegen::decode_borrowed_request_view(
                        &body,
                        ctx.decode_options(),
                    )?;
                    let req = ::connectrpc::ServiceRequest::<
                        crate::proto::app::task::v1::CancelRunRequest,
                    >::from_parts(&req, &body);
                    svc.cancel_run(ctx, req)
                        .await?
                        .encode::<crate::proto::app::task::v1::CancelRunResponse>(format)
                })
            }
            "ReplayRun" => {
                let svc = ::std::sync::Arc::clone(&self.inner);
                Box::pin(async move {
                    let body = ::connectrpc::dispatcher::codegen::request_proto_bytes::<
                        crate::proto::app::task::v1::ReplayRunRequest,
                    >(request.encoded()?, format)?;
                    let req: crate::proto::app::task::v1::__buffa::view::ReplayRunRequestView<
                        '_,
                    > = ::connectrpc::dispatcher::codegen::decode_borrowed_request_view(
                        &body,
                        ctx.decode_options(),
                    )?;
                    let req = ::connectrpc::ServiceRequest::<
                        crate::proto::app::task::v1::ReplayRunRequest,
                    >::from_parts(&req, &body);
                    svc.replay_run(ctx, req)
                        .await?
                        .encode::<crate::proto::app::task::v1::ReplayRunResponse>(format)
                })
            }
            "RescheduleRun" => {
                let svc = ::std::sync::Arc::clone(&self.inner);
                Box::pin(async move {
                    let body = ::connectrpc::dispatcher::codegen::request_proto_bytes::<
                        crate::proto::app::task::v1::RescheduleRunRequest,
                    >(request.encoded()?, format)?;
                    let req: crate::proto::app::task::v1::__buffa::view::RescheduleRunRequestView<
                        '_,
                    > = ::connectrpc::dispatcher::codegen::decode_borrowed_request_view(
                        &body,
                        ctx.decode_options(),
                    )?;
                    let req = ::connectrpc::ServiceRequest::<
                        crate::proto::app::task::v1::RescheduleRunRequest,
                    >::from_parts(&req, &body);
                    svc.reschedule_run(ctx, req)
                        .await?
                        .encode::<
                            crate::proto::app::task::v1::RescheduleRunResponse,
                        >(format)
                })
            }
            _ => ::connectrpc::dispatcher::codegen::unimplemented_unary(path),
        }
    }
    fn call_server_streaming(
        &self,
        path: &str,
        ctx: ::connectrpc::RequestContext,
        request: ::buffa::bytes::Bytes,
        format: ::connectrpc::CodecFormat,
    ) -> ::connectrpc::dispatcher::codegen::StreamingResult {
        let Some(method) = path.strip_prefix("app.task.v1.TaskService/") else {
            return ::connectrpc::dispatcher::codegen::unimplemented_streaming(path);
        };
        let _ = (&ctx, &request, &format);
        match method {
            _ => ::connectrpc::dispatcher::codegen::unimplemented_streaming(path),
        }
    }
    fn call_client_streaming(
        &self,
        path: &str,
        ctx: ::connectrpc::RequestContext,
        requests: ::connectrpc::dispatcher::codegen::RequestStream,
        format: ::connectrpc::CodecFormat,
    ) -> ::connectrpc::dispatcher::codegen::UnaryResult {
        let Some(method) = path.strip_prefix("app.task.v1.TaskService/") else {
            return ::connectrpc::dispatcher::codegen::unimplemented_unary(path);
        };
        let _ = (&ctx, &requests, &format);
        match method {
            _ => ::connectrpc::dispatcher::codegen::unimplemented_unary(path),
        }
    }
    fn call_bidi_streaming(
        &self,
        path: &str,
        ctx: ::connectrpc::RequestContext,
        requests: ::connectrpc::dispatcher::codegen::RequestStream,
        format: ::connectrpc::CodecFormat,
    ) -> ::connectrpc::dispatcher::codegen::StreamingResult {
        let Some(method) = path.strip_prefix("app.task.v1.TaskService/") else {
            return ::connectrpc::dispatcher::codegen::unimplemented_streaming(path);
        };
        let _ = (&ctx, &requests, &format);
        match method {
            _ => ::connectrpc::dispatcher::codegen::unimplemented_streaming(path),
        }
    }
}
/// Client for this service.
///
/// Generic over `T: ClientTransport`. For **gRPC** (HTTP/2), use
/// `Http2Connection` — it has honest `poll_ready` and composes with
/// `tower::balance` for multi-connection load balancing. For **Connect
/// over HTTP/1.1** (or unknown protocol), use `HttpClient`.
///
/// # Example (gRPC / HTTP/2)
///
/// ```rust,ignore
/// use connectrpc::client::{Http2Connection, ClientConfig};
/// use connectrpc::Protocol;
///
/// let uri: http::Uri = "http://localhost:8080".parse()?;
/// let conn = Http2Connection::connect_plaintext(uri.clone()).await?.shared(1024);
/// let config = ClientConfig::new(uri).with_protocol(Protocol::Grpc);
///
/// let client = TaskServiceClient::new(conn, config);
/// let response = client.trigger(request).await?;
/// ```
///
/// # Example (Connect / HTTP/1.1 or ALPN)
///
/// ```rust,ignore
/// use connectrpc::client::{HttpClient, ClientConfig};
///
/// let http = HttpClient::plaintext();  // cleartext http:// only
/// let config = ClientConfig::new("http://localhost:8080".parse()?);
///
/// let client = TaskServiceClient::new(http, config);
/// let response = client.trigger(request).await?;
/// ```
///
/// # Working with the response
///
/// Unary calls return [`UnaryResponse<OwnedView<FooView>>`](::connectrpc::client::UnaryResponse).
/// [`view()`](::connectrpc::client::UnaryResponse::view) borrows the response
/// message, so field access is zero-copy:
///
/// ```rust,ignore
/// let resp = client.trigger(request).await?;
/// let name: &str = resp.view().name;  // borrow into the response buffer
/// ```
///
/// If you need the owned struct (e.g. to store or pass by value), use
/// [`into_owned()`](::connectrpc::client::UnaryResponse::into_owned):
///
/// ```rust,ignore
/// let owned = client.trigger(request).await?.into_owned();
/// ```
///
/// [`into_view()`](::connectrpc::client::UnaryResponse::into_view) keeps the
/// zero-copy decoded body (an `OwnedView`) without copying; field access on it
/// goes through `.reborrow()`. Streaming responses yield one
/// [`StreamMessage`](::connectrpc::StreamMessage) per received message from
/// `.message().await` — read fields zero-copy through the generated accessor
/// methods (`msg.name()`) or `.view()`, or convert with `.to_owned_message()`.
#[derive(Clone)]
pub struct TaskServiceClient<T> {
    transport: T,
    config: ::connectrpc::client::ClientConfig,
}
impl<T> TaskServiceClient<T>
where
    T: ::connectrpc::client::ClientTransport,
    <T::ResponseBody as ::connectrpc::http_body::Body>::Error: ::std::fmt::Display,
{
    /// Create a new client with the given transport and configuration.
    pub fn new(transport: T, config: ::connectrpc::client::ClientConfig) -> Self {
        Self { transport, config }
    }
    /// Get the client configuration.
    pub fn config(&self) -> &::connectrpc::client::ClientConfig {
        &self.config
    }
    /// Get a mutable reference to the client configuration.
    pub fn config_mut(&mut self) -> &mut ::connectrpc::client::ClientConfig {
        &mut self.config
    }
    /// Call the Trigger RPC. Sends a request to /app.task.v1.TaskService/Trigger.
    pub async fn trigger(
        &self,
        request: crate::proto::app::task::v1::TriggerRequest,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::TriggerResponseView<'static>,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        self.trigger_with_options(request, ::connectrpc::client::CallOptions::default())
            .await
    }
    /// Call the Trigger RPC with explicit per-call options. Options override [`ClientConfig`](::connectrpc::client::ClientConfig) defaults.
    pub async fn trigger_with_options(
        &self,
        request: crate::proto::app::task::v1::TriggerRequest,
        options: ::connectrpc::client::CallOptions,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::TriggerResponseView<'static>,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        ::connectrpc::client::call_unary(
                &self.transport,
                &self.config,
                TASK_SERVICE_TRIGGER_SPEC.with_origin(::connectrpc::SpecOrigin::Client),
                request,
                options,
            )
            .await
    }
    /// Call the BatchTrigger RPC. Sends a request to /app.task.v1.TaskService/BatchTrigger.
    pub async fn batch_trigger(
        &self,
        request: crate::proto::app::task::v1::BatchTriggerRequest,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::BatchTriggerResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        self.batch_trigger_with_options(
                request,
                ::connectrpc::client::CallOptions::default(),
            )
            .await
    }
    /// Call the BatchTrigger RPC with explicit per-call options. Options override [`ClientConfig`](::connectrpc::client::ClientConfig) defaults.
    pub async fn batch_trigger_with_options(
        &self,
        request: crate::proto::app::task::v1::BatchTriggerRequest,
        options: ::connectrpc::client::CallOptions,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::BatchTriggerResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        ::connectrpc::client::call_unary(
                &self.transport,
                &self.config,
                TASK_SERVICE_BATCH_TRIGGER_SPEC
                    .with_origin(::connectrpc::SpecOrigin::Client),
                request,
                options,
            )
            .await
    }
    /// Call the RetrieveRun RPC. Sends a request to /app.task.v1.TaskService/RetrieveRun.
    pub async fn retrieve_run(
        &self,
        request: crate::proto::app::task::v1::RetrieveRunRequest,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::RetrieveRunResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        self.retrieve_run_with_options(
                request,
                ::connectrpc::client::CallOptions::default(),
            )
            .await
    }
    /// Call the RetrieveRun RPC with explicit per-call options. Options override [`ClientConfig`](::connectrpc::client::ClientConfig) defaults.
    pub async fn retrieve_run_with_options(
        &self,
        request: crate::proto::app::task::v1::RetrieveRunRequest,
        options: ::connectrpc::client::CallOptions,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::RetrieveRunResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        ::connectrpc::client::call_unary(
                &self.transport,
                &self.config,
                TASK_SERVICE_RETRIEVE_RUN_SPEC
                    .with_origin(::connectrpc::SpecOrigin::Client),
                request,
                options,
            )
            .await
    }
    /// Call the ListRuns RPC. Sends a request to /app.task.v1.TaskService/ListRuns.
    pub async fn list_runs(
        &self,
        request: crate::proto::app::task::v1::ListRunsRequest,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::ListRunsResponseView<'static>,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        self.list_runs_with_options(
                request,
                ::connectrpc::client::CallOptions::default(),
            )
            .await
    }
    /// Call the ListRuns RPC with explicit per-call options. Options override [`ClientConfig`](::connectrpc::client::ClientConfig) defaults.
    pub async fn list_runs_with_options(
        &self,
        request: crate::proto::app::task::v1::ListRunsRequest,
        options: ::connectrpc::client::CallOptions,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::ListRunsResponseView<'static>,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        ::connectrpc::client::call_unary(
                &self.transport,
                &self.config,
                TASK_SERVICE_LIST_RUNS_SPEC
                    .with_origin(::connectrpc::SpecOrigin::Client),
                request,
                options,
            )
            .await
    }
    /// Call the CancelRun RPC. Sends a request to /app.task.v1.TaskService/CancelRun.
    pub async fn cancel_run(
        &self,
        request: crate::proto::app::task::v1::CancelRunRequest,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::CancelRunResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        self.cancel_run_with_options(
                request,
                ::connectrpc::client::CallOptions::default(),
            )
            .await
    }
    /// Call the CancelRun RPC with explicit per-call options. Options override [`ClientConfig`](::connectrpc::client::ClientConfig) defaults.
    pub async fn cancel_run_with_options(
        &self,
        request: crate::proto::app::task::v1::CancelRunRequest,
        options: ::connectrpc::client::CallOptions,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::CancelRunResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        ::connectrpc::client::call_unary(
                &self.transport,
                &self.config,
                TASK_SERVICE_CANCEL_RUN_SPEC
                    .with_origin(::connectrpc::SpecOrigin::Client),
                request,
                options,
            )
            .await
    }
    /// Call the ReplayRun RPC. Sends a request to /app.task.v1.TaskService/ReplayRun.
    pub async fn replay_run(
        &self,
        request: crate::proto::app::task::v1::ReplayRunRequest,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::ReplayRunResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        self.replay_run_with_options(
                request,
                ::connectrpc::client::CallOptions::default(),
            )
            .await
    }
    /// Call the ReplayRun RPC with explicit per-call options. Options override [`ClientConfig`](::connectrpc::client::ClientConfig) defaults.
    pub async fn replay_run_with_options(
        &self,
        request: crate::proto::app::task::v1::ReplayRunRequest,
        options: ::connectrpc::client::CallOptions,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::ReplayRunResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        ::connectrpc::client::call_unary(
                &self.transport,
                &self.config,
                TASK_SERVICE_REPLAY_RUN_SPEC
                    .with_origin(::connectrpc::SpecOrigin::Client),
                request,
                options,
            )
            .await
    }
    /// Call the RescheduleRun RPC. Sends a request to /app.task.v1.TaskService/RescheduleRun.
    pub async fn reschedule_run(
        &self,
        request: crate::proto::app::task::v1::RescheduleRunRequest,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::RescheduleRunResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        self.reschedule_run_with_options(
                request,
                ::connectrpc::client::CallOptions::default(),
            )
            .await
    }
    /// Call the RescheduleRun RPC with explicit per-call options. Options override [`ClientConfig`](::connectrpc::client::ClientConfig) defaults.
    pub async fn reschedule_run_with_options(
        &self,
        request: crate::proto::app::task::v1::RescheduleRunRequest,
        options: ::connectrpc::client::CallOptions,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::task::v1::__buffa::view::RescheduleRunResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        ::connectrpc::client::call_unary(
                &self.transport,
                &self.config,
                TASK_SERVICE_RESCHEDULE_RUN_SPEC
                    .with_origin(::connectrpc::SpecOrigin::Client),
                request,
                options,
            )
            .await
    }
}
