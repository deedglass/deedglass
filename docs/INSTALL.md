# Install Deedglass

Deedglass is one binary, `deedglass`: the server, the CLI and the MCP bridge. Pick the way that suits
the machine. Releases start at 0.1.0; until the first one is published, build from source
(`make build`) or use `deedglass init` from a checkout.

## Docker

```sh
docker run --rm ghcr.io/deedglass/deedglass:0.1.0 version
```

The image is distroless, runs as user 65532, and is built for linux/amd64 and linux/arm64. In
production, pin it by digest: `ghcr.io/deedglass/deedglass:0.1.0@sha256:<digest>` (the release
notes list the digest). Prereleases (`0.1.0-rc.1`) never move the `latest` or `0.1` tags.

## A binary

Download the archive for your platform from the release page:
`deedglass_<version>_<os>_<arch>.tar.gz` for linux and darwin on amd64 and arm64, or
`deedglass_<version>_windows_amd64.zip`. Check it (next section), unpack it, and put `deedglass` on
your PATH. Each archive holds the binary, LICENSE, README.md, CHANGELOG.md and shell completions.

## Homebrew

Once the tap exists: `brew install deedglass/tap/deedglass`. The binaries aren't notarized yet, so
the cask removes macOS's quarantine attribute from the one binary it installs.

## go install

```sh
go install github.com/deedglass/deedglass/cmd/deedglass@latest
```

This builds the CLI without the Console. It runs every CLI command, and `serve` in dev mode; a prod
server needs a release binary or image, which carry the Console.

## Check what you downloaded

Each release lists sha256 checksums. From 0.1.0 on, once the repository is public, the checksum file
is also signed in the open Sigstore log by this repository's release workflow. Here is how to check
both before you run the binary.

```sh
shasum -a 256 --ignore-missing -c checksums.txt
```

Expected: `deedglass_<version>_<os>_<arch>.tar.gz: OK`. Then, for a signed release, with
[cosign](https://docs.sigstore.dev/cosign/system_config/installation/) 3.x:

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/deedglass/deedglass/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Expected: `Verified OK`. That says the checksum file was signed by this repository's release
workflow for a version tag; it says nothing about what the code does. Each archive also has an SPDX
SBOM next to it (`<archive>.sbom.json`) listing the Go modules inside.

## Shell completions

The archives carry them in `completions/`. Or generate them from any binary:

```sh
deedglass completion zsh > ~/.zfunc/_deedglass        # with fpath+=(~/.zfunc) before compinit
deedglass completion bash > ~/.local/share/bash-completion/completions/deedglass
deedglass completion fish > ~/.config/fish/completions/deedglass.fish
```

## Check for a newer version

```sh
deedglass version --check
```

It asks GitHub's releases API once (3 s, only a User-Agent) and never runs on its own. Exit 0 when
the check ran, 2 when it couldn't tell.

## Windows

The CLI, `verify` and the MCP bridge run on Windows. `serve` is untested there; run the server on
Linux or macOS, or in Docker.

## What it doesn't do

- Releases aren't signed while the repository is private, and macOS binaries aren't notarized.
- There are no OS packages (deb, rpm) or a Windows installer yet.
