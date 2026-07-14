# Build stage
FROM golang:1.26-alpine AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /app/bin/server ./cmd/server

# Runtime stage — distroless/static:nonroot (no shell, no package manager, uid 65532)
FROM gcr.io/distroless/static:nonroot

WORKDIR /app

COPY --from=builder /app/bin/server .
COPY --from=builder /app/internal/repository/postgres/migrations ./migrations

EXPOSE 8080

ENTRYPOINT ["./server"]
