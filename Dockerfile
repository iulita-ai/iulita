# --- Stage 1: Build frontend ---
FROM node:22-alpine3.24@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202 AS ui-builder

RUN apk upgrade --no-cache

WORKDIR /app/ui
COPY ui/package.json ui/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY ui/ ./
RUN npm run build

# --- Stage 2: Build Go binary ---
FROM golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS go-builder

ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

RUN apk upgrade --no-cache && apk add --no-cache gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
COPY third_party/fosite ./third_party/fosite
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

# Exported separately for CI; never included in the production runtime image.
RUN CGO_ENABLED=1 go build -trimpath -o /imagecheck ./cmd/imagecheck/

# Pre-fetch the ONNX embedding model into the image (requires network at build
# time) so the first runtime start needs no download. Baked OUTSIDE the data
# volume; consumed read-only via IULITA_MODEL_PRELOAD_DIR in the runtime stage.
RUN /iulita download-model /opt/iulita/models

# --- Stage 3: Runtime ---
FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS runtime

# ffmpeg: voice transcription transcodes Telegram's OGG/Opus to mp3 —
# GLM-ASR (and some other Whisper-compatible endpoints) only accept wav/mp3.
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates tzdata ffmpeg poppler-utils

RUN adduser -D -u 1000 -h /app iulita
WORKDIR /app

COPY --from=go-builder /iulita /usr/local/bin/iulita

# ONNX embedding model baked into the image (read-only), consumed via
# IULITA_MODEL_PRELOAD_DIR so startup needs no network and survives an empty
# data volume / PVC. Lives outside /app/data so the volume mount can't hide it.
COPY --from=go-builder --chown=iulita:iulita /opt/iulita/models /opt/iulita/models
ENV IULITA_MODEL_PRELOAD_DIR=/opt/iulita/models

# Default skills directory and example config.
COPY skills/ ./skills/
COPY config.toml.example ./config.toml.example

# Pre-create data directory for SQLite + ONNX models.
RUN mkdir -p /app/data && chown iulita:iulita /app/data

USER iulita

# /app/data — SQLite database (+ ONNX model copy if downloaded at runtime).
# The embedding model itself is baked into the image at IULITA_MODEL_PRELOAD_DIR.
VOLUME ["/app/data"]

EXPOSE 8080

ENTRYPOINT ["iulita", "--server"]

FROM scratch AS smoke-tools
COPY --from=go-builder /imagecheck /imagecheck

FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS gateway-check-builder
WORKDIR /check
COPY cmd/gatewaycheck/main.go ./main.go
RUN CGO_ENABLED=0 go build -trimpath -o /gatewaycheck main.go

FROM scratch AS gateway-tools
COPY --from=gateway-check-builder /gatewaycheck /gatewaycheck

# Keep the production image the default target for existing docker build users.
FROM runtime AS production
