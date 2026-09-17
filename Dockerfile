# syntax=docker/dockerfile:1
FROM dhi.io/golang:1 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /src/out/server ./cmd/server && \
    CGO_ENABLED=0 go build -o /src/out/healthcheck ./cmd/healthcheck

FROM dhi.io/static:20260611-alpine
WORKDIR /app
COPY --from=build /src/out/server /app/server
COPY --from=build /src/out/healthcheck /app/healthcheck
EXPOSE 8080
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/healthcheck"]
ENTRYPOINT ["/app/server"]
