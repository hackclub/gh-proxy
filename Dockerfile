# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS build
RUN apk add --no-cache git build-base
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build /out/server /app/server
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/app/server"]
