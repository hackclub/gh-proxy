# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
RUN apk add --no-cache git build-base
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -o /out/healthcheck ./cmd/healthcheck

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build /out/server /app/server
COPY --from=build /out/healthcheck /app/healthcheck
EXPOSE 8080
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/healthcheck"]
ENTRYPOINT ["/app/server"]
