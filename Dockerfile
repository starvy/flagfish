# syntax=docker/dockerfile:1.9

# The product ships as ONE static binary (R3): the SPA and the goose migrations
# are go:embed'd, and nothing is read from disk at runtime. _arch/06 states the
# acceptance test in as many words:
#
#     "If `./flagfish serve` cannot run from an empty directory against a fresh
#      Postgres, R3 is broken."
#
# So the final stage is distroless/static — no shell, no package manager, no libc.
# There is nothing in the image to read a config file FROM, which is the point:
# the constraint is enforced by the image, not by a code review.

# ---------------------------------------------------------------------------
# 1. web — React + Vite + TanStack (D7), built to web/dist
# ---------------------------------------------------------------------------
FROM node:24-alpine AS web

WORKDIR /web

# Copy manifests first so `npm ci` is cached independently of source changes.
# The trailing `./` + wildcard tolerates web/ being empty for now: COPY fails on
# NO matches, but `package*.json*` matches zero files without erroring.
COPY web/package*.json* web/

RUN if [ -f web/package.json ]; then \
      cd web && npm ci --no-audit --no-fund; \
    else \
      echo ">> web/ is empty — skipping npm ci"; \
    fi

COPY web/ web/

# Build the SPA when it is present; otherwise leave web/dist empty and let the
# committed placeholder under internal/web/dist carry the go:embed.
RUN if [ -f web/package.json ]; then \
      cd web && npm run build; \
    else \
      mkdir -p web/dist; \
    fi

# ---------------------------------------------------------------------------
# 2. build — the static Go binary
# ---------------------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# git is only needed for the version stamp below.
RUN apk add --no-cache git ca-certificates

# Module graph first: this layer is invalidated only by a dependency change,
# not by every edit to a handler.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# Stage the built SPA where the go:embed directive reads it. The trailing `/.`
# copies the contents over the committed placeholder without removing it.
COPY --from=web /web/web/dist/. ./internal/web/dist/

ARG VERSION=dev
ARG COMMIT=unknown

# CGO_ENABLED=0 is what makes `distroless/static` viable: no dynamic loader, no
# libc, nothing to CVE-scan. -trimpath keeps the build reproducible.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/flagfish \
      ./cmd/flagfish

# Fail the build here, not in production, if the binary is not actually static.
RUN ! ldd /out/flagfish 2>/dev/null | grep -q "=>" || (echo "FATAL: binary is dynamically linked" && exit 1)

# ---------------------------------------------------------------------------
# 3. runtime — distroless/static, nonroot
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

# ca-certificates: outbound TLS for webhooks and SMTP (the River jobs, D5).
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/flagfish /flagfish

USER nonroot:nonroot
# Documentation only (EXPOSE does not publish). Matches FLAGFISH_ADDR's default :8000;
# override both together if you rebind the listener.
EXPOSE 8000

# _arch/06: "serve --with-worker  # both, in-process. THE DEFAULT."
# Self-hosters get one container. Large events override the command to split the
# roles — the topology is a flag, not a rebuild.
ENTRYPOINT ["/flagfish"]
CMD ["serve", "--with-worker"]

# distroless has no shell and no curl, so a HEALTHCHECK must be the binary
# itself. Left to the orchestrator (compose/k8s probe the HTTP endpoint) rather
# than baked in wrong.
LABEL org.opencontainers.image.title="flagfish" \
      org.opencontainers.image.description="A CTF platform in Go" \
      org.opencontainers.image.source="https://github.com/starvy/flagfish" \
      org.opencontainers.image.licenses="Apache-2.0"
