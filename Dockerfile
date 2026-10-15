# The Deedglass server image: a static Go binary on distroless static-debian13, running as the
# non-root user 65532. Both base images are pinned by the digest of their multi-arch index
# (ADR-024 records each digest and the date it was looked up). No secret ever enters a layer:
# .dockerignore keeps .env, .deedglass/ and key files out of the build context.
#
#   make docker                                   # deedglass:dev
#   docker build -t deedglass:dev .               # the same, without version build args
#
# Publishing and multi-arch images are S35; this file builds for the host platform only.

FROM golang:1.27.2-trixie@sha256:e58d6f83b3416618d8bcac2b3dde1b7f7e3c4a77d25e88637f8bbae81536c48d AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY . .
ARG VERSION=0.1.0-dev COMMIT=unknown DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath $(sh scripts/gcflags.sh) -ldflags "-s -w \
      -X github.com/deedglass/deedglass/internal/buildinfo.Version=${VERSION} \
      -X github.com/deedglass/deedglass/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/deedglass/deedglass/internal/buildinfo.Date=${DATE}" -o /out/deedglass ./cmd/deedglass
# An empty .deedglass/ (mode 0700) for the server's working directory. The compose file mounts a named
# volume here so a dev server on a read-only root filesystem can create and keep its dev seal key; a
# new volume copies this directory's owner and mode. Nothing secret is in it at build time.
RUN mkdir -p /out/home/.deedglass && chmod 0700 /out/home/.deedglass

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/deedglass /deedglass
COPY --from=build --chown=65532:65532 /out/home/ /home/nonroot/
USER 65532:65532
ENV DEEDGLASS_LISTEN=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/deedglass"]
CMD ["serve"]
LABEL org.opencontainers.image.title="Deedglass" \
      org.opencontainers.image.description="The execution and accountability layer for AI agents" \
      org.opencontainers.image.source="https://github.com/deedglass/deedglass" \
      org.opencontainers.image.licenses="Apache-2.0"
