# deedglass

The Deedglass CLI on npm. `npx -y deedglass scan` grades what your agent setup can change (A to F),
locally and read-only. It connects only to a database you name with `--postgres-dsn-env`, and sends
nothing anywhere else.

```sh
npx -y deedglass scan
npx -y deedglass scan --postgres-dsn-env DATABASE_URL
```

npm installs one platform package (`@deedglass/cli-<os>-<arch>`) with the binary; there is no install
script. Without a matching platform: `go install github.com/deedglass/deedglass/cmd/deedglass@latest`.

Docs: https://docs.deedglass.com/scan

Licence: this package, the launcher, is Apache-2.0. The binary it installs (`@deedglass/cli-<os>-<arch>`)
includes the Pro features in `ee/`, so the platform packages' licence is `Apache-2.0 AND FSL-1.1-ALv2`,
and each carries both texts (`LICENSE`, `ee/LICENSE`). https://github.com/deedglass/deedglass#license
