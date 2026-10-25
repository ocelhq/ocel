FROM node:22-slim
RUN apt-get update && apt-get install --yes --no-install-recommends python3 make g++ \
    && rm -rf /var/lib/apt/lists/*
RUN npm install --global pnpm@11.10.0
WORKDIR /app
COPY . .
RUN pnpm install --frozen-lockfile
WORKDIR /app/tests/fixtures/deploy/next
RUN pnpm run build
CMD ["pnpm", "run", "start"]
