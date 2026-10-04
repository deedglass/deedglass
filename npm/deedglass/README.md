# deedglass

The Deedglass CLI on npm. `npx -y deedglass scan` grades what your agent setup can change (A to F),
locally and read-only; nothing is sent anywhere.

```sh
npx -y deedglass scan
npx -y deedglass scan --postgres-dsn-env DATABASE_URL
```

npm installs one platform package (`@deedglass/cli-<os>-<arch>`) with the binary; there is no install
script. Without a matching platform: `go install github.com/deedglass/deedglass/cmd/deedglass@latest`.

Docs: https://docs.deedglass.com/scan · Licence: Apache-2.0
