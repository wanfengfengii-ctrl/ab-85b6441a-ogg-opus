# syntax=docker/dockerfile:1

# ---- builder: full Go toolchain, source and tests -------------------------
FROM golang:1.27-alpine AS builder
WORKDIR /src
# The module uses only the Go standard library, so no dependency download is
# needed; copy everything and build both binaries.
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/smoke ./cmd/smoke

# ---- runtime: minimal image that actually serves the API -----------------
FROM alpine:3.20 AS runtime
# ca-certificates in case the service is ever fronted by TLS plumbing.
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /out/server /app/server
ENV OPUS_AUDIT_ADDR=:8080
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/app/server"]

# ---- verify: one-shot image carrying the toolchain and source -------------
# Used by the Compose "verify" service: runs the Go tests, the production
# build, and the HTTP smoke suite against the running api service.
FROM golang:1.27-alpine AS verify
WORKDIR /src
COPY . .
RUN chmod +x scripts/verify.sh
ENTRYPOINT ["/src/scripts/verify.sh"]
