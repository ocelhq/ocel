FROM node:22-slim
RUN npm install --global pnpm@11.10.0
WORKDIR /workspace
COPY . .
RUN pnpm install --frozen-lockfile
WORKDIR /workspace/tests/fixtures/prerender/next-dockerfile
ARG OCEL_LIVE_HASH
RUN --mount=type=secret,id=OCEL_RESOURCE_POSTGRES_main,target=/run/ocel-live/OCEL_RESOURCE_POSTGRES_main,required=false \
    --network=host \
    OCEL_LIVE_DIR=/run/ocel-live pnpm run build
CMD ["pnpm", "run", "start"]
