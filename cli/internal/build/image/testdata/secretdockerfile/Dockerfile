FROM alpine:3.20
ARG OCEL_LIVE_HASH
RUN --mount=type=secret,id=OCEL_RESOURCE_POSTGRES_my-db,target=/run/ocel-live/OCEL_RESOURCE_POSTGRES_my-db \
    --network=host \
    export OCEL_LIVE_DIR=/run/ocel-live \
    && sha256sum "$OCEL_LIVE_DIR/OCEL_RESOURCE_POSTGRES_my-db" | cut -d' ' -f1 > /proof \
    && port="$(sed 's/.*:\([0-9]*\)\/db$/\1/' "$OCEL_LIVE_DIR/OCEL_RESOURCE_POSTGRES_my-db")" \
    && wget -qO- "http://127.0.0.1:$port/" > /reached
CMD ["sh"]
