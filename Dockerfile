# Build stage
FROM golang:1.26-alpine@sha256:28d89ee9cc0ff9fec75c82ca201e6bf7fdf9a679d4b7b24dfa04f2bb766bb468 AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /app/bin/server ./cmd/server

# Runtime stage — distroless/static:nonroot (no shell, no package manager, uid 65532)
FROM gcr.io/distroless/static:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7

WORKDIR /app

COPY --from=builder /app/bin/server .
COPY --from=builder /app/internal/repository/postgres/migrations ./migrations

EXPOSE 8080

ENTRYPOINT ["./server"]
