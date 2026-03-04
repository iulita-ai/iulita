# --- Stage 1: Build frontend ---
FROM node:22-alpine AS ui-builder

WORKDIR /app/ui
COPY ui/package.json ui/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY ui/ ./
RUN npm run build

# --- Stage 2: Build Go binary ---
FROM golang:1.25-alpine AS go-builder

ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

RUN apk add --no-cache gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Inject built frontend into embed directory.
COPY --from=ui-builder /app/ui/dist ./ui/dist

RUN CGO_ENABLED=1 go build \
    -ldflags="-s -w \
      -X github.com/iulita-ai/iulita/internal/version.Version=${VERSION} \
      -X github.com/iulita-ai/iulita/internal/version.Commit=${COMMIT} \
      -X github.com/iulita-ai/iulita/internal/version.Date=${DATE}" \
    -o /iulita ./cmd/iulita/

# --- Stage 3: Runtime ---
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

RUN adduser -D -u 1000 -h /app iulita
WORKDIR /app

COPY --from=go-builder /iulita /usr/local/bin/iulita

# Default skills directory and example config.
COPY skills/ ./skills/
COPY config.toml.example ./config.toml.example

# Pre-create data directory for SQLite + ONNX models.
RUN mkdir -p /app/data && chown iulita:iulita /app/data

USER iulita

# /app/data — SQLite database + ONNX embedding models (downloaded on first run, ~90MB)
VOLUME ["/app/data"]

EXPOSE 8080

ENTRYPOINT ["iulita", "--server"]
