# The Deedglass server image: a static Go binary on distroless static-debian13, running as the
# non-root user 65532. Both base images are pinned by the digest of their multi-arch index
# (ADR-024 records each digest and the date it was looked up). No secret ever enters a layer:
# .dockerignore keeps .env, .deedglass/ and key files out of the build context.
#
#   make docker                                   # deedglass:dev
#   docker build -t deedglass:dev .               # the same, without version build args
#
# Publishing and multi-arch images are S35; this file builds for the host platform only.

FROM golang:1.27.1-trixie@sha256:3b77fc618ec235a1ab412de7737f120dd507c57e8d87de4cbb7994fb94275ed5 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY . .
ARG VERSION=0.1.0-dev COMMIT=unknown DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w \
      -X github.com/deedglass/deedglass/internal/buildinfo.Version=${VERSION} \
      -X github.com/deedglass/deedglass/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/deedglass/deedglass/internal/buildinfo.Date=${DATE}" -o /out/deedglass ./cmd/deedglass

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/deedglass /deedglass
USER 65532:65532
ENV DEEDGLASS_LISTEN=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/deedglass"]
CMD ["serve"]
LABEL org.opencontainers.image.title="Deedglass" \
      org.opencontainers.image.description="The execution and accountability layer for AI agents" \
      org.opencontainers.image.source="https://github.com/deedglass/deedglass" \
      org.opencontainers.image.licenses="Apache-2.0"
