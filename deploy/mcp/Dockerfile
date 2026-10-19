# The Deedglass MCP server image for the Docker MCP Catalog (S47 §3.6): built from source, distroless,
# non-root (65532), entrypoint `deedglass mcp serve`. It holds no key: the catalog passes
# DEEDGLASS_API_KEY as a secret and DEEDGLASS_SERVER as configuration at run time.
#
#   docker build -f deploy/mcp/Dockerfile -t deedglass-mcp:dev .
FROM golang:1.27.2-trixie@sha256:e58d6f83b3416618d8bcac2b3dde1b7f7e3c4a77d25e88637f8bbae81536c48d AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
ARG VERSION=0.3.0
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/deedglass/deedglass/internal/buildinfo.Version=${VERSION}" -o /out/deedglass ./cmd/deedglass

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/deedglass /usr/local/bin/deedglass
USER 65532:65532
ENTRYPOINT ["deedglass", "mcp", "serve"]
LABEL org.opencontainers.image.title="Deedglass MCP server" \
      org.opencontainers.image.source="https://github.com/deedglass/deedglass" \
      org.opencontainers.image.licenses="Apache-2.0 AND FSL-1.1-ALv2" \
      io.modelcontextprotocol.server.name="io.github.deedglass/deedglass"
