FROM golang:1.27-alpine AS build

ENV GOWORK=off CGO_ENABLED=0
WORKDIR /src

COPY pkg ./pkg
COPY platform ./platform
COPY pricing ./pricing

WORKDIR /src/pricing
RUN go build -trimpath -ldflags="-s -w" -o /out/pricing ./cmd/pricing

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/pricing /usr/local/bin/pricing

EXPOSE 8090
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/pricing"]
