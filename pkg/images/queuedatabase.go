package images

const queueDatabaseImage = "ghcr.io/pgmq/pg18-pgmq:v1.13.0@sha256:2dd8ac92a1c0eb121d6ea5b12b3f7c015813ae58945a940451d4683afd5a19c2"

func QueueDatabase() string { return queueDatabaseImage }
