FROM node:22-slim
RUN apt-get update && apt-get install --yes --no-install-recommends python3 make g++ \
    && rm -rf /var/lib/apt/lists/*
RUN npm install --global pnpm@11.10.0
WORKDIR /app
COPY . .
RUN pnpm install --frozen-lockfile
WORKDIR /app/tests/fixtures/sdk/next
ARG OCEL_LIVE_HASH
RUN --mount=type=secret,id=OCEL_RESOURCE_POSTGRES_main,target=/run/ocel-live/OCEL_RESOURCE_POSTGRES_main,required=false \
    --mount=type=secret,id=OCEL_RESOURCE_BUCKET_uploads,target=/run/ocel-live/OCEL_RESOURCE_BUCKET_uploads,required=false \
    --mount=type=secret,id=BUILD_SENSITIVE,target=/run/ocel-live/BUILD_SENSITIVE,required=false \
    --mount=type=secret,id=BUILD_SECRET,target=/run/ocel-live/BUILD_SECRET,required=false \
    --mount=type=secret,id=OCEL_RUNTIME_ADDRESS,env=OCEL_RUNTIME_ADDRESS,required=false \
    --mount=type=secret,id=OCEL_SESSION_TOKEN,env=OCEL_SESSION_TOKEN,required=false \
    --network=host \
    OCEL_LIVE_DIR=/run/ocel-live pnpm run build
CMD ["pnpm", "run", "start"]
