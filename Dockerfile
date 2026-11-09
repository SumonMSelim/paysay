# syntax=docker/dockerfile:1

# --- bryntum stage -------------------------------------------------------
# Bryntum Scheduler trial bundle is fetched at build time under its commercial
# trial licence. Only prebuilt browser assets needed by the app are copied
# into the web assets directory before go build.
FROM node:22-alpine AS bryntum

WORKDIR /bryntum
RUN npm install @bryntum/scheduler@npm:@bryntum/scheduler-trial@7.3.8
RUN mkdir -p /out/bryntum/locales && \
    cd node_modules/@bryntum/scheduler && \
    cp scheduler.umd.js scheduler.css stockholm-light.css /out/bryntum/ && \
    cp -r fontawesome /out/bryntum/ && \
    cp -r fonts /out/bryntum/ && \
    cp locales/scheduler.locale.De.js locales/scheduler.locale.Th.js locales/scheduler.locale.En.js /out/bryntum/locales/

# --- build stage ---------------------------------------------------------
# Compile the Go binary with the web assets embedded. CGO is disabled for a
# static binary that runs on the slim runtime image.
FROM golang:1.27-alpine AS build

WORKDIR /src

# Cache module downloads independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=bryntum /out/bryntum /src/internal/web/assets/vendor/bryntum

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/paysay ./cmd/paysay

# --- runtime stage -------------------------------------------------------
# The PayPal MCP server runs as `npx @paypal/mcp`, so the runtime image needs
# Node.js and npm available on PATH. node:alpine provides both and is small.
FROM node:22-alpine AS runtime

# Create a non-root user to run the service.
RUN addgroup -S paysay && adduser -S paysay -G paysay

WORKDIR /app
COPY --from=build /out/paysay /app/paysay

# Pre-warm the PayPal MCP package into the image so the first request does not
# pay the npx download cost. This is best-effort; if offline at build time the
# package is fetched on first use instead.
RUN npm install -g @paypal/mcp || true

USER paysay

EXPOSE 8080
ENV SERVER_ADDR=:8080

# Simple healthcheck against the liveness endpoint.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/app/paysay"]
