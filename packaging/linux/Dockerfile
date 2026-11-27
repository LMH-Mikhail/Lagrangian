# Multi-stage Dockerfile for running Lagrangian as a headless Linux always-on node.
FROM node:22-alpine AS frontend-builder
WORKDIR /app/frontend
RUN corepack enable
COPY frontend/package.json frontend/pnpm-lock.yaml* frontend/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile || pnpm install
COPY frontend/ ./
RUN pnpm build

FROM golang:1.26-alpine AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=frontend-builder /app/frontend/dist ./internal/transport/httpapi/webdist/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lagrangian-server ./cmd/lagrangian-server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates git tzdata \
    && adduser -D -u 10001 lagrangian \
    && mkdir -p /data && chown -R lagrangian:lagrangian /data
COPY --from=go-builder /out/lagrangian-server /usr/local/bin/lagrangian-server
USER lagrangian
ENV LAGRANGIAN_HOME=/data \
    LAGRANGIAN_SECRET_BACKEND=file
VOLUME ["/data"]
EXPOSE 19000
ENTRYPOINT ["/usr/local/bin/lagrangian-server", "--home", "/data", "--host", "0.0.0.0", "--port", "19000"]
