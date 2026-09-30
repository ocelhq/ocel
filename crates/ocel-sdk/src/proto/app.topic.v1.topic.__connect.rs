///Shorthand for `OwnedView<SendRequestView<'static>>`.
pub type OwnedSendRequestView = ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::SendRequestView<'static>,
>;
///Shorthand for `OwnedView<SendResponseView<'static>>`.
pub type OwnedSendResponseView = ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::SendResponseView<'static>,
>;
///Shorthand for `OwnedView<ListDeadLettersRequestView<'static>>`.
pub type OwnedListDeadLettersRequestView = ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::ListDeadLettersRequestView<'static>,
>;
///Shorthand for `OwnedView<ListDeadLettersResponseView<'static>>`.
pub type OwnedListDeadLettersResponseView = ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::ListDeadLettersResponseView<'static>,
>;
///Shorthand for `OwnedView<RedriveDeadLettersRequestView<'static>>`.
pub type OwnedRedriveDeadLettersRequestView = ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::RedriveDeadLettersRequestView<'static>,
>;
///Shorthand for `OwnedView<RedriveDeadLettersResponseView<'static>>`.
pub type OwnedRedriveDeadLettersResponseView = ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::RedriveDeadLettersResponseView<'static>,
>;
///Shorthand for `OwnedView<PurgeDeadLettersRequestView<'static>>`.
pub type OwnedPurgeDeadLettersRequestView = ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::PurgeDeadLettersRequestView<'static>,
>;
///Shorthand for `OwnedView<PurgeDeadLettersResponseView<'static>>`.
pub type OwnedPurgeDeadLettersResponseView = ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::PurgeDeadLettersResponseView<'static>,
>;
///Shorthand for `OwnedView<CountDeadLettersRequestView<'static>>`.
pub type OwnedCountDeadLettersRequestView = ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::CountDeadLettersRequestView<'static>,
>;
///Shorthand for `OwnedView<CountDeadLettersResponseView<'static>>`.
pub type OwnedCountDeadLettersResponseView = ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::CountDeadLettersResponseView<'static>,
>;
impl ::connectrpc::Encodable<crate::proto::app::topic::v1::SendResponse>
for crate::proto::app::topic::v1::__buffa::view::SendResponseView<'_> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self, codec)
    }
}
impl ::connectrpc::Encodable<crate::proto::app::topic::v1::SendResponse>
for ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::SendResponseView<'static>,
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
impl ::connectrpc::Encodable<crate::proto::app::topic::v1::ListDeadLettersResponse>
for crate::proto::app::topic::v1::__buffa::view::ListDeadLettersResponseView<'_> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self, codec)
    }
}
impl ::connectrpc::Encodable<crate::proto::app::topic::v1::ListDeadLettersResponse>
for ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::ListDeadLettersResponseView<'static>,
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
impl ::connectrpc::Encodable<crate::proto::app::topic::v1::RedriveDeadLettersResponse>
for crate::proto::app::topic::v1::__buffa::view::RedriveDeadLettersResponseView<'_> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self, codec)
    }
}
impl ::connectrpc::Encodable<crate::proto::app::topic::v1::RedriveDeadLettersResponse>
for ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::RedriveDeadLettersResponseView<'static>,
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
impl ::connectrpc::Encodable<crate::proto::app::topic::v1::PurgeDeadLettersResponse>
for crate::proto::app::topic::v1::__buffa::view::PurgeDeadLettersResponseView<'_> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self, codec)
    }
}
impl ::connectrpc::Encodable<crate::proto::app::topic::v1::PurgeDeadLettersResponse>
for ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::PurgeDeadLettersResponseView<'static>,
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
impl ::connectrpc::Encodable<crate::proto::app::topic::v1::CountDeadLettersResponse>
for crate::proto::app::topic::v1::__buffa::view::CountDeadLettersResponseView<'_> {
    fn encode(
        &self,
        codec: ::connectrpc::CodecFormat,
    ) -> ::std::result::Result<::buffa::bytes::Bytes, ::connectrpc::ConnectError> {
        ::connectrpc::__codegen::encode_view_body(self, codec)
    }
}
impl ::connectrpc::Encodable<crate::proto::app::topic::v1::CountDeadLettersResponse>
for ::buffa::view::OwnedView<
    crate::proto::app::topic::v1::__buffa::view::CountDeadLettersResponseView<'static>,
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
pub const TOPIC_SERVICE_SERVICE_NAME: &str = "app.topic.v1.TopicService";
/// Static [`Spec`](::connectrpc::Spec) for the `Send` RPC, as seen by the server; the generated client passes it with [`origin`](::connectrpc::Spec::origin) `Client` (compare across sides with [`Spec::same_method`](::connectrpc::Spec::same_method)).
pub const TOPIC_SERVICE_SEND_SPEC: ::connectrpc::Spec = ::connectrpc::Spec::server(
        "/app.topic.v1.TopicService/Send",
        ::connectrpc::StreamType::Unary,
    )
    .with_idempotency_level(::connectrpc::IdempotencyLevel::Unknown);
/// Static [`Spec`](::connectrpc::Spec) for the `ListDeadLetters` RPC, as seen by the server; the generated client passes it with [`origin`](::connectrpc::Spec::origin) `Client` (compare across sides with [`Spec::same_method`](::connectrpc::Spec::same_method)).
pub const TOPIC_SERVICE_LIST_DEAD_LETTERS_SPEC: ::connectrpc::Spec = ::connectrpc::Spec::server(
        "/app.topic.v1.TopicService/ListDeadLetters",
        ::connectrpc::StreamType::Unary,
    )
    .with_idempotency_level(::connectrpc::IdempotencyLevel::Unknown);
/// Static [`Spec`](::connectrpc::Spec) for the `RedriveDeadLetters` RPC, as seen by the server; the generated client passes it with [`origin`](::connectrpc::Spec::origin) `Client` (compare across sides with [`Spec::same_method`](::connectrpc::Spec::same_method)).
pub const TOPIC_SERVICE_REDRIVE_DEAD_LETTERS_SPEC: ::connectrpc::Spec = ::connectrpc::Spec::server(
        "/app.topic.v1.TopicService/RedriveDeadLetters",
        ::connectrpc::StreamType::Unary,
    )
    .with_idempotency_level(::connectrpc::IdempotencyLevel::Unknown);
/// Static [`Spec`](::connectrpc::Spec) for the `PurgeDeadLetters` RPC, as seen by the server; the generated client passes it with [`origin`](::connectrpc::Spec::origin) `Client` (compare across sides with [`Spec::same_method`](::connectrpc::Spec::same_method)).
pub const TOPIC_SERVICE_PURGE_DEAD_LETTERS_SPEC: ::connectrpc::Spec = ::connectrpc::Spec::server(
        "/app.topic.v1.TopicService/PurgeDeadLetters",
        ::connectrpc::StreamType::Unary,
    )
    .with_idempotency_level(::connectrpc::IdempotencyLevel::Unknown);
/// Static [`Spec`](::connectrpc::Spec) for the `CountDeadLetters` RPC, as seen by the server; the generated client passes it with [`origin`](::connectrpc::Spec::origin) `Client` (compare across sides with [`Spec::same_method`](::connectrpc::Spec::same_method)).
pub const TOPIC_SERVICE_COUNT_DEAD_LETTERS_SPEC: ::connectrpc::Spec = ::connectrpc::Spec::server(
        "/app.topic.v1.TopicService/CountDeadLetters",
        ::connectrpc::StreamType::Unary,
    )
    .with_idempotency_level(::connectrpc::IdempotencyLevel::Unknown);
/// Server trait for TopicService.
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
pub trait TopicService: Send + Sync + 'static {
    /// Handle the Send RPC.
    ///
    /// `'a` lets the response body borrow from `&self` (e.g. server-resident state).
    ///
    /// `request` is borrowed from the request body and is valid for the
    /// duration of the call; message fields are read directly on it
    /// (zero-copy). The response cannot borrow from `request` — use
    /// `.to_owned_message()` (or copy the specific fields) for anything
    /// returned, stored, or moved into `tokio::spawn`.
    fn send<'a>(
        &'a self,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::ServiceRequest<
            '_,
            crate::proto::app::topic::v1::SendRequest,
        >,
    ) -> impl ::std::future::Future<
        Output = ::connectrpc::ServiceResult<
            impl ::connectrpc::Encodable<
                crate::proto::app::topic::v1::SendResponse,
            > + Send + use<'a, Self>,
        >,
    > + Send;
    /// Handle the ListDeadLetters RPC.
    ///
    /// `'a` lets the response body borrow from `&self` (e.g. server-resident state).
    ///
    /// `request` is borrowed from the request body and is valid for the
    /// duration of the call; message fields are read directly on it
    /// (zero-copy). The response cannot borrow from `request` — use
    /// `.to_owned_message()` (or copy the specific fields) for anything
    /// returned, stored, or moved into `tokio::spawn`.
    fn list_dead_letters<'a>(
        &'a self,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::ServiceRequest<
            '_,
            crate::proto::app::topic::v1::ListDeadLettersRequest,
        >,
    ) -> impl ::std::future::Future<
        Output = ::connectrpc::ServiceResult<
            impl ::connectrpc::Encodable<
                crate::proto::app::topic::v1::ListDeadLettersResponse,
            > + Send + use<'a, Self>,
        >,
    > + Send;
    /// Handle the RedriveDeadLetters RPC.
    ///
    /// `'a` lets the response body borrow from `&self` (e.g. server-resident state).
    ///
    /// `request` is borrowed from the request body and is valid for the
    /// duration of the call; message fields are read directly on it
    /// (zero-copy). The response cannot borrow from `request` — use
    /// `.to_owned_message()` (or copy the specific fields) for anything
    /// returned, stored, or moved into `tokio::spawn`.
    fn redrive_dead_letters<'a>(
        &'a self,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::ServiceRequest<
            '_,
            crate::proto::app::topic::v1::RedriveDeadLettersRequest,
        >,
    ) -> impl ::std::future::Future<
        Output = ::connectrpc::ServiceResult<
            impl ::connectrpc::Encodable<
                crate::proto::app::topic::v1::RedriveDeadLettersResponse,
            > + Send + use<'a, Self>,
        >,
    > + Send;
    /// Handle the PurgeDeadLetters RPC.
    ///
    /// `'a` lets the response body borrow from `&self` (e.g. server-resident state).
    ///
    /// `request` is borrowed from the request body and is valid for the
    /// duration of the call; message fields are read directly on it
    /// (zero-copy). The response cannot borrow from `request` — use
    /// `.to_owned_message()` (or copy the specific fields) for anything
    /// returned, stored, or moved into `tokio::spawn`.
    fn purge_dead_letters<'a>(
        &'a self,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::ServiceRequest<
            '_,
            crate::proto::app::topic::v1::PurgeDeadLettersRequest,
        >,
    ) -> impl ::std::future::Future<
        Output = ::connectrpc::ServiceResult<
            impl ::connectrpc::Encodable<
                crate::proto::app::topic::v1::PurgeDeadLettersResponse,
            > + Send + use<'a, Self>,
        >,
    > + Send;
    /// Handle the CountDeadLetters RPC.
    ///
    /// `'a` lets the response body borrow from `&self` (e.g. server-resident state).
    ///
    /// `request` is borrowed from the request body and is valid for the
    /// duration of the call; message fields are read directly on it
    /// (zero-copy). The response cannot borrow from `request` — use
    /// `.to_owned_message()` (or copy the specific fields) for anything
    /// returned, stored, or moved into `tokio::spawn`.
    fn count_dead_letters<'a>(
        &'a self,
        ctx: ::connectrpc::RequestContext,
        request: ::connectrpc::ServiceRequest<
            '_,
            crate::proto::app::topic::v1::CountDeadLettersRequest,
        >,
    ) -> impl ::std::future::Future<
        Output = ::connectrpc::ServiceResult<
            impl ::connectrpc::Encodable<
                crate::proto::app::topic::v1::CountDeadLettersResponse,
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
pub trait TopicServiceExt: TopicService {
    /// Register this service implementation with a Router.
    ///
    /// Takes ownership of the `Arc<Self>` and returns a new Router with
    /// this service's methods registered.
    fn register(
        self: ::std::sync::Arc<Self>,
        router: ::connectrpc::Router,
    ) -> ::connectrpc::Router;
}
impl<S: TopicService> TopicServiceExt for S {
    fn register(
        self: ::std::sync::Arc<Self>,
        router: ::connectrpc::Router,
    ) -> ::connectrpc::Router {
        router
            .route_view(
                TOPIC_SERVICE_SERVICE_NAME,
                "Send",
                {
                    let svc = ::std::sync::Arc::clone(&self);
                    ::connectrpc::view_handler_fn(move |
                        ctx,
                        req: ::buffa::view::OwnedView<
                            crate::proto::app::topic::v1::__buffa::view::SendRequestView<
                                'static,
                            >,
                        >,
                        format|
                    {
                        let svc = ::std::sync::Arc::clone(&svc);
                        async move {
                            let sreq = ::connectrpc::ServiceRequest::<
                                crate::proto::app::topic::v1::SendRequest,
                            >::from_parts(req.reborrow(), req.bytes());
                            svc.send(ctx, sreq)
                                .await?
                                .encode::<
                                    crate::proto::app::topic::v1::SendResponse,
                                >(format)
                        }
                    })
                },
            )
            .with_spec(TOPIC_SERVICE_SEND_SPEC)
            .route_view(
                TOPIC_SERVICE_SERVICE_NAME,
                "ListDeadLetters",
                {
                    let svc = ::std::sync::Arc::clone(&self);
                    ::connectrpc::view_handler_fn(move |
                        ctx,
                        req: ::buffa::view::OwnedView<
                            crate::proto::app::topic::v1::__buffa::view::ListDeadLettersRequestView<
                                'static,
                            >,
                        >,
                        format|
                    {
                        let svc = ::std::sync::Arc::clone(&svc);
                        async move {
                            let sreq = ::connectrpc::ServiceRequest::<
                                crate::proto::app::topic::v1::ListDeadLettersRequest,
                            >::from_parts(req.reborrow(), req.bytes());
                            svc.list_dead_letters(ctx, sreq)
                                .await?
                                .encode::<
                                    crate::proto::app::topic::v1::ListDeadLettersResponse,
                                >(format)
                        }
                    })
                },
            )
            .with_spec(TOPIC_SERVICE_LIST_DEAD_LETTERS_SPEC)
            .route_view(
                TOPIC_SERVICE_SERVICE_NAME,
                "RedriveDeadLetters",
                {
                    let svc = ::std::sync::Arc::clone(&self);
                    ::connectrpc::view_handler_fn(move |
                        ctx,
                        req: ::buffa::view::OwnedView<
                            crate::proto::app::topic::v1::__buffa::view::RedriveDeadLettersRequestView<
                                'static,
                            >,
                        >,
                        format|
                    {
                        let svc = ::std::sync::Arc::clone(&svc);
                        async move {
                            let sreq = ::connectrpc::ServiceRequest::<
                                crate::proto::app::topic::v1::RedriveDeadLettersRequest,
                            >::from_parts(req.reborrow(), req.bytes());
                            svc.redrive_dead_letters(ctx, sreq)
                                .await?
                                .encode::<
                                    crate::proto::app::topic::v1::RedriveDeadLettersResponse,
                                >(format)
                        }
                    })
                },
            )
            .with_spec(TOPIC_SERVICE_REDRIVE_DEAD_LETTERS_SPEC)
            .route_view(
                TOPIC_SERVICE_SERVICE_NAME,
                "PurgeDeadLetters",
                {
                    let svc = ::std::sync::Arc::clone(&self);
                    ::connectrpc::view_handler_fn(move |
                        ctx,
                        req: ::buffa::view::OwnedView<
                            crate::proto::app::topic::v1::__buffa::view::PurgeDeadLettersRequestView<
                                'static,
                            >,
                        >,
                        format|
                    {
                        let svc = ::std::sync::Arc::clone(&svc);
                        async move {
                            let sreq = ::connectrpc::ServiceRequest::<
                                crate::proto::app::topic::v1::PurgeDeadLettersRequest,
                            >::from_parts(req.reborrow(), req.bytes());
                            svc.purge_dead_letters(ctx, sreq)
                                .await?
                                .encode::<
                                    crate::proto::app::topic::v1::PurgeDeadLettersResponse,
                                >(format)
                        }
                    })
                },
            )
            .with_spec(TOPIC_SERVICE_PURGE_DEAD_LETTERS_SPEC)
            .route_view(
                TOPIC_SERVICE_SERVICE_NAME,
                "CountDeadLetters",
                {
                    let svc = ::std::sync::Arc::clone(&self);
                    ::connectrpc::view_handler_fn(move |
                        ctx,
                        req: ::buffa::view::OwnedView<
                            crate::proto::app::topic::v1::__buffa::view::CountDeadLettersRequestView<
                                'static,
                            >,
                        >,
                        format|
                    {
                        let svc = ::std::sync::Arc::clone(&svc);
                        async move {
                            let sreq = ::connectrpc::ServiceRequest::<
                                crate::proto::app::topic::v1::CountDeadLettersRequest,
                            >::from_parts(req.reborrow(), req.bytes());
                            svc.count_dead_letters(ctx, sreq)
                                .await?
                                .encode::<
                                    crate::proto::app::topic::v1::CountDeadLettersResponse,
                                >(format)
                        }
                    })
                },
            )
            .with_spec(TOPIC_SERVICE_COUNT_DEAD_LETTERS_SPEC)
    }
}
/// Type-inference marker used by [`Router::add_service`](::connectrpc::Router::add_service).
#[doc(hidden)]
pub struct TopicServiceRegisterMarker;
impl<S: TopicService> ::connectrpc::ServiceRegister<TopicServiceRegisterMarker>
for ::std::sync::Arc<S> {
    fn register_service(self, router: ::connectrpc::Router) -> ::connectrpc::Router {
        <S as TopicServiceExt>::register(self, router)
    }
}
/// Monomorphic dispatcher for `TopicService`.
///
/// Unlike `.register(Router)` which type-erases each method into an `Arc<dyn ErasedHandler>` stored in a `HashMap`, this struct dispatches via a compile-time `match` on method name: no vtable, no hash lookup.
///
/// # Example
///
/// ```rust,ignore
/// use connectrpc::ConnectRpcService;
///
/// let server = TopicServiceServer::new(MyImpl);
/// let service = ConnectRpcService::new(server);
/// // hand `service` to axum/hyper as a fallback_service
/// ```
pub struct TopicServiceServer<T> {
    inner: ::std::sync::Arc<T>,
}
impl<T: TopicService> TopicServiceServer<T> {
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
impl<T> Clone for TopicServiceServer<T> {
    fn clone(&self) -> Self {
        Self {
            inner: ::std::sync::Arc::clone(&self.inner),
        }
    }
}
impl<T: TopicService> ::connectrpc::Dispatcher for TopicServiceServer<T> {
    #[inline]
    fn lookup(
        &self,
        path: &str,
    ) -> Option<::connectrpc::dispatcher::codegen::MethodDescriptor> {
        let method = path.strip_prefix("app.topic.v1.TopicService/")?;
        match method {
            "Send" => {
                Some(
                    ::connectrpc::dispatcher::codegen::MethodDescriptor::unary(false)
                        .with_spec(TOPIC_SERVICE_SEND_SPEC),
                )
            }
            "ListDeadLetters" => {
                Some(
                    ::connectrpc::dispatcher::codegen::MethodDescriptor::unary(false)
                        .with_spec(TOPIC_SERVICE_LIST_DEAD_LETTERS_SPEC),
                )
            }
            "RedriveDeadLetters" => {
                Some(
                    ::connectrpc::dispatcher::codegen::MethodDescriptor::unary(false)
                        .with_spec(TOPIC_SERVICE_REDRIVE_DEAD_LETTERS_SPEC),
                )
            }
            "PurgeDeadLetters" => {
                Some(
                    ::connectrpc::dispatcher::codegen::MethodDescriptor::unary(false)
                        .with_spec(TOPIC_SERVICE_PURGE_DEAD_LETTERS_SPEC),
                )
            }
            "CountDeadLetters" => {
                Some(
                    ::connectrpc::dispatcher::codegen::MethodDescriptor::unary(false)
                        .with_spec(TOPIC_SERVICE_COUNT_DEAD_LETTERS_SPEC),
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
        let Some(method) = path.strip_prefix("app.topic.v1.TopicService/") else {
            return ::connectrpc::dispatcher::codegen::unimplemented_unary(path);
        };
        let _ = (&ctx, &request, &format);
        match method {
            "Send" => {
                let svc = ::std::sync::Arc::clone(&self.inner);
                Box::pin(async move {
                    let body = ::connectrpc::dispatcher::codegen::request_proto_bytes::<
                        crate::proto::app::topic::v1::SendRequest,
                    >(request.encoded()?, format)?;
                    let req: crate::proto::app::topic::v1::__buffa::view::SendRequestView<
                        '_,
                    > = ::connectrpc::dispatcher::codegen::decode_borrowed_request_view(
                        &body,
                        ctx.decode_options(),
                    )?;
                    let req = ::connectrpc::ServiceRequest::<
                        crate::proto::app::topic::v1::SendRequest,
                    >::from_parts(&req, &body);
                    svc.send(ctx, req)
                        .await?
                        .encode::<crate::proto::app::topic::v1::SendResponse>(format)
                })
            }
            "ListDeadLetters" => {
                let svc = ::std::sync::Arc::clone(&self.inner);
                Box::pin(async move {
                    let body = ::connectrpc::dispatcher::codegen::request_proto_bytes::<
                        crate::proto::app::topic::v1::ListDeadLettersRequest,
                    >(request.encoded()?, format)?;
                    let req: crate::proto::app::topic::v1::__buffa::view::ListDeadLettersRequestView<
                        '_,
                    > = ::connectrpc::dispatcher::codegen::decode_borrowed_request_view(
                        &body,
                        ctx.decode_options(),
                    )?;
                    let req = ::connectrpc::ServiceRequest::<
                        crate::proto::app::topic::v1::ListDeadLettersRequest,
                    >::from_parts(&req, &body);
                    svc.list_dead_letters(ctx, req)
                        .await?
                        .encode::<
                            crate::proto::app::topic::v1::ListDeadLettersResponse,
                        >(format)
                })
            }
            "RedriveDeadLetters" => {
                let svc = ::std::sync::Arc::clone(&self.inner);
                Box::pin(async move {
                    let body = ::connectrpc::dispatcher::codegen::request_proto_bytes::<
                        crate::proto::app::topic::v1::RedriveDeadLettersRequest,
                    >(request.encoded()?, format)?;
                    let req: crate::proto::app::topic::v1::__buffa::view::RedriveDeadLettersRequestView<
                        '_,
                    > = ::connectrpc::dispatcher::codegen::decode_borrowed_request_view(
                        &body,
                        ctx.decode_options(),
                    )?;
                    let req = ::connectrpc::ServiceRequest::<
                        crate::proto::app::topic::v1::RedriveDeadLettersRequest,
                    >::from_parts(&req, &body);
                    svc.redrive_dead_letters(ctx, req)
                        .await?
                        .encode::<
                            crate::proto::app::topic::v1::RedriveDeadLettersResponse,
                        >(format)
                })
            }
            "PurgeDeadLetters" => {
                let svc = ::std::sync::Arc::clone(&self.inner);
                Box::pin(async move {
                    let body = ::connectrpc::dispatcher::codegen::request_proto_bytes::<
                        crate::proto::app::topic::v1::PurgeDeadLettersRequest,
                    >(request.encoded()?, format)?;
                    let req: crate::proto::app::topic::v1::__buffa::view::PurgeDeadLettersRequestView<
                        '_,
                    > = ::connectrpc::dispatcher::codegen::decode_borrowed_request_view(
                        &body,
                        ctx.decode_options(),
                    )?;
                    let req = ::connectrpc::ServiceRequest::<
                        crate::proto::app::topic::v1::PurgeDeadLettersRequest,
                    >::from_parts(&req, &body);
                    svc.purge_dead_letters(ctx, req)
                        .await?
                        .encode::<
                            crate::proto::app::topic::v1::PurgeDeadLettersResponse,
                        >(format)
                })
            }
            "CountDeadLetters" => {
                let svc = ::std::sync::Arc::clone(&self.inner);
                Box::pin(async move {
                    let body = ::connectrpc::dispatcher::codegen::request_proto_bytes::<
                        crate::proto::app::topic::v1::CountDeadLettersRequest,
                    >(request.encoded()?, format)?;
                    let req: crate::proto::app::topic::v1::__buffa::view::CountDeadLettersRequestView<
                        '_,
                    > = ::connectrpc::dispatcher::codegen::decode_borrowed_request_view(
                        &body,
                        ctx.decode_options(),
                    )?;
                    let req = ::connectrpc::ServiceRequest::<
                        crate::proto::app::topic::v1::CountDeadLettersRequest,
                    >::from_parts(&req, &body);
                    svc.count_dead_letters(ctx, req)
                        .await?
                        .encode::<
                            crate::proto::app::topic::v1::CountDeadLettersResponse,
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
        let Some(method) = path.strip_prefix("app.topic.v1.TopicService/") else {
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
        let Some(method) = path.strip_prefix("app.topic.v1.TopicService/") else {
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
        let Some(method) = path.strip_prefix("app.topic.v1.TopicService/") else {
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
/// let client = TopicServiceClient::new(conn, config);
/// let response = client.send(request).await?;
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
/// let client = TopicServiceClient::new(http, config);
/// let response = client.send(request).await?;
/// ```
///
/// # Working with the response
///
/// Unary calls return [`UnaryResponse<OwnedView<FooView>>`](::connectrpc::client::UnaryResponse).
/// [`view()`](::connectrpc::client::UnaryResponse::view) borrows the response
/// message, so field access is zero-copy:
///
/// ```rust,ignore
/// let resp = client.send(request).await?;
/// let name: &str = resp.view().name;  // borrow into the response buffer
/// ```
///
/// If you need the owned struct (e.g. to store or pass by value), use
/// [`into_owned()`](::connectrpc::client::UnaryResponse::into_owned):
///
/// ```rust,ignore
/// let owned = client.send(request).await?.into_owned();
/// ```
///
/// [`into_view()`](::connectrpc::client::UnaryResponse::into_view) keeps the
/// zero-copy decoded body (an `OwnedView`) without copying; field access on it
/// goes through `.reborrow()`. Streaming responses yield one
/// [`StreamMessage`](::connectrpc::StreamMessage) per received message from
/// `.message().await` — read fields zero-copy through the generated accessor
/// methods (`msg.name()`) or `.view()`, or convert with `.to_owned_message()`.
#[derive(Clone)]
pub struct TopicServiceClient<T> {
    transport: T,
    config: ::connectrpc::client::ClientConfig,
}
impl<T> TopicServiceClient<T>
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
    /// Call the Send RPC. Sends a request to /app.topic.v1.TopicService/Send.
    pub async fn send(
        &self,
        request: crate::proto::app::topic::v1::SendRequest,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::topic::v1::__buffa::view::SendResponseView<'static>,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        self.send_with_options(request, ::connectrpc::client::CallOptions::default())
            .await
    }
    /// Call the Send RPC with explicit per-call options. Options override [`ClientConfig`](::connectrpc::client::ClientConfig) defaults.
    pub async fn send_with_options(
        &self,
        request: crate::proto::app::topic::v1::SendRequest,
        options: ::connectrpc::client::CallOptions,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::topic::v1::__buffa::view::SendResponseView<'static>,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        ::connectrpc::client::call_unary(
                &self.transport,
                &self.config,
                TOPIC_SERVICE_SEND_SPEC.with_origin(::connectrpc::SpecOrigin::Client),
                request,
                options,
            )
            .await
    }
    /// Call the ListDeadLetters RPC. Sends a request to /app.topic.v1.TopicService/ListDeadLetters.
    pub async fn list_dead_letters(
        &self,
        request: crate::proto::app::topic::v1::ListDeadLettersRequest,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::topic::v1::__buffa::view::ListDeadLettersResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        self.list_dead_letters_with_options(
                request,
                ::connectrpc::client::CallOptions::default(),
            )
            .await
    }
    /// Call the ListDeadLetters RPC with explicit per-call options. Options override [`ClientConfig`](::connectrpc::client::ClientConfig) defaults.
    pub async fn list_dead_letters_with_options(
        &self,
        request: crate::proto::app::topic::v1::ListDeadLettersRequest,
        options: ::connectrpc::client::CallOptions,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::topic::v1::__buffa::view::ListDeadLettersResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        ::connectrpc::client::call_unary(
                &self.transport,
                &self.config,
                TOPIC_SERVICE_LIST_DEAD_LETTERS_SPEC
                    .with_origin(::connectrpc::SpecOrigin::Client),
                request,
                options,
            )
            .await
    }
    /// Call the RedriveDeadLetters RPC. Sends a request to /app.topic.v1.TopicService/RedriveDeadLetters.
    pub async fn redrive_dead_letters(
        &self,
        request: crate::proto::app::topic::v1::RedriveDeadLettersRequest,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::topic::v1::__buffa::view::RedriveDeadLettersResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        self.redrive_dead_letters_with_options(
                request,
                ::connectrpc::client::CallOptions::default(),
            )
            .await
    }
    /// Call the RedriveDeadLetters RPC with explicit per-call options. Options override [`ClientConfig`](::connectrpc::client::ClientConfig) defaults.
    pub async fn redrive_dead_letters_with_options(
        &self,
        request: crate::proto::app::topic::v1::RedriveDeadLettersRequest,
        options: ::connectrpc::client::CallOptions,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::topic::v1::__buffa::view::RedriveDeadLettersResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        ::connectrpc::client::call_unary(
                &self.transport,
                &self.config,
                TOPIC_SERVICE_REDRIVE_DEAD_LETTERS_SPEC
                    .with_origin(::connectrpc::SpecOrigin::Client),
                request,
                options,
            )
            .await
    }
    /// Call the PurgeDeadLetters RPC. Sends a request to /app.topic.v1.TopicService/PurgeDeadLetters.
    pub async fn purge_dead_letters(
        &self,
        request: crate::proto::app::topic::v1::PurgeDeadLettersRequest,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::topic::v1::__buffa::view::PurgeDeadLettersResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        self.purge_dead_letters_with_options(
                request,
                ::connectrpc::client::CallOptions::default(),
            )
            .await
    }
    /// Call the PurgeDeadLetters RPC with explicit per-call options. Options override [`ClientConfig`](::connectrpc::client::ClientConfig) defaults.
    pub async fn purge_dead_letters_with_options(
        &self,
        request: crate::proto::app::topic::v1::PurgeDeadLettersRequest,
        options: ::connectrpc::client::CallOptions,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::topic::v1::__buffa::view::PurgeDeadLettersResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        ::connectrpc::client::call_unary(
                &self.transport,
                &self.config,
                TOPIC_SERVICE_PURGE_DEAD_LETTERS_SPEC
                    .with_origin(::connectrpc::SpecOrigin::Client),
                request,
                options,
            )
            .await
    }
    /// Call the CountDeadLetters RPC. Sends a request to /app.topic.v1.TopicService/CountDeadLetters.
    pub async fn count_dead_letters(
        &self,
        request: crate::proto::app::topic::v1::CountDeadLettersRequest,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::topic::v1::__buffa::view::CountDeadLettersResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        self.count_dead_letters_with_options(
                request,
                ::connectrpc::client::CallOptions::default(),
            )
            .await
    }
    /// Call the CountDeadLetters RPC with explicit per-call options. Options override [`ClientConfig`](::connectrpc::client::ClientConfig) defaults.
    pub async fn count_dead_letters_with_options(
        &self,
        request: crate::proto::app::topic::v1::CountDeadLettersRequest,
        options: ::connectrpc::client::CallOptions,
    ) -> Result<
        ::connectrpc::client::UnaryResponse<
            ::buffa::view::OwnedView<
                crate::proto::app::topic::v1::__buffa::view::CountDeadLettersResponseView<
                    'static,
                >,
            >,
        >,
        ::connectrpc::ConnectError,
    > {
        ::connectrpc::client::call_unary(
                &self.transport,
                &self.config,
                TOPIC_SERVICE_COUNT_DEAD_LETTERS_SPEC
                    .with_origin(::connectrpc::SpecOrigin::Client),
                request,
                options,
            )
            .await
    }
}
