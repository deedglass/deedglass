# Logs and metrics

`deedglass serve` writes one log line per request, plus lines for failures. It can also serve Prometheus metrics on a separate listener. Both are built so that secrets and
request bodies never leave the process. The code is `internal/httpx` (`log.go`, `metrics.go`,
`server.go`).

## Log format

| Setting | Effect |
|---|---|
| `DEEDGLASS_ENV=prod` | JSON, one object per line (`log/slog` JSON handler). |
| `DEEDGLASS_ENV=dev` | Text, `key=value` pairs (`log/slog` text handler). |
| `DEEDGLASS_LOG_LEVEL` | `debug`, `info` (default), `warn` or `error`. |

Access lines are written at `info`. At `warn` or `error` they are skipped; metrics still count
every request.

A request in prod:

```json
{"time":"2026-10-01T09:30:00.123Z","level":"INFO","msg":"request","request_id":"req_01j9zr3m5k8q2w7x4t6v0b1c9d","method":"GET","route":"/v1/health","path":"/v1/health","status":200,"bytes":312,"duration_ms":0.412,"ip":"127.0.0.1","org_id":"","agent_id":"","action_id":"","api_version":"2026-10-01","idempotent_replay":false,"client_request_id":"","user_agent":"curl/8.7.1"}
```

The same request in dev:

```
time=2026-10-01T09:30:00.123Z level=INFO msg=request request_id=req_01j9zr3m5k8q2w7x4t6v0b1c9d method=GET route=/v1/health path=/v1/health status=200 bytes=312 duration_ms=0.412 ip=127.0.0.1 org_id="" agent_id="" action_id="" api_version=2026-10-01 idempotent_replay=false client_request_id="" user_agent=curl/8.7.1
```

## Access-log fields

Every field is written on every line, in this order. A value that doesn't apply is an empty
string.

| Field | Value |
|---|---|
| `request_id` | The `req_…` id, the same as the `Deedglass-Request-Id` response header. |
| `method` | The request method as sent. |
| `route` | The matched route pattern, such as `/v1/actions/{id}`, or `unmatched` for 404 and 405 answers. |
| `path` | The request path, without the query string. Clipped to 200 characters (then `…`); bytes outside printable ASCII become `?`. |
| `status` | The final status. `499` when the response was aborted mid-write (`http.ErrAbortHandler`). |
| `bytes` | Response body bytes written. |
| `duration_ms` | Time to serve the request, in milliseconds with microsecond precision. |
| `ip` | The client address. `X-Forwarded-For` counts only when the connection comes from `DEEDGLASS_TRUSTED_PROXIES`; the list is read right to left, skipping trusted hops. |
| `org_id` | The caller's org, once authenticated. |
| `agent_id` | The caller's agent, for API keys that belong to one. |
| `action_id` | The action the request touched, when its handler records one. |
| `api_version` | The resolved `Deedglass-Version`. |
| `idempotent_replay` | `true` when the response was a stored replay. |
| `client_request_id` | The inbound `X-Request-Id`, when it is 1–64 printable ASCII characters. |
| `user_agent` | The `User-Agent`, cut to 128 bytes. |

## Other log lines

| Message | Level | Fields | When |
|---|---|---|---|
| `panic serving request` | error | `request_id`, `route`, `panic`, `stack` | A handler panicked. The client got 500 `internal_error`; the stack is only here. |
| `internal error` | error | `request_id`, `error` | A handler returned an error that isn't an API error. |
| `request failed` | debug (4xx), error (5xx) | `request_id`, `route`, `code`, `cause` | An API error with an underlying cause. |
| `request timed out` | warn | `request_id`, `route`, `timeout_ms` | 504 `timeout`. |
| `handler returned an error after writing the response` | warn | `request_id`, `route`, `error` | A handler bug: the client already has a response. |
| `idempotency: storing the response failed` | error | `request_id`, `error` | The response was sent but not stored; a retry runs again. |
| `idempotency: releasing the key failed` | error | `request_id`, `error` | A 429 or 503 couldn't free its key; a retry gets 409 until the lock runs out. |
| `idempotency: response too large to store; key released` | warn | `request_id`, `bytes` | A response over 1 MiB; a retry runs again. |
| `health: database check failed` | warn | `error` | `GET /v1/health` reported the database `down`. |
| `health: migration check failed` | warn | `error` | `GET /v1/health` reported migrations `unknown`. |

The health endpoint shows statuses only. The error text behind them is in these lines.

## Redaction

Every logger the server builds is wrapped in a redacting handler (`NewRedactingHandler`). It runs
on every record, at every level, before anything is written.

**Dropped attributes.** An attribute with one of these names is removed, whatever its value. Names
are compared without regard to case, inside groups too:

`authorization`, `cookie`, `set-cookie`, `password`, `secret`, `token`, `api_key`, `dsn`,
`database_url`

**Scrubbed values.** In the message, in string values, in error values and in values with a
`String` method:

| Pattern | Becomes |
|---|---|
| `dg_live_…` or `dg_test_…` (letters, digits, `_`) | `dg_live_[redacted]` or `dg_test_[redacted]` |
| Any URL userinfo: `://user:pass@`, `://user@` or `://token@` | `://[redacted]@` |

**Never logged.** Request bodies, response bodies and the query string. The access line has the
path only.

Tests plant a canary API key in an `Authorization` header and a canary password in a database URL,
then check that neither appears in the captured log.

## Metrics

Metrics are off by default. To turn them on, set `DEEDGLASS_METRICS_LISTEN` to a host and port.
Bind it to localhost, or to a private interface your Prometheus can reach:

```sh
DEEDGLASS_METRICS_LISTEN=127.0.0.1:9464 deedglass serve
curl -s http://127.0.0.1:9464/metrics
```

The listener serves `GET` and `HEAD /metrics` only, in the Prometheus text format 0.0.4
(`Content-Type: text/plain; version=0.0.4; charset=utf-8`, `Cache-Control: no-store`). It has no
authentication, which is why it stays off the API port.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `deedglass_http_requests_total` | counter | `route`, `method`, `status` | Requests served. |
| `deedglass_http_request_duration_seconds` | histogram | `route` | Time to serve a request. |
| `deedglass_http_in_flight` | gauge | none | Requests being served now. |
| `deedglass_build_info` | gauge, always `1` | `version`, `commit` | The build this server runs. |

**Labels are bounded.** `route` is the route pattern (`/v1/actions/{id}`), never a raw path, and
`unmatched` for every request no route matched. `method` is `GET`, `HEAD`, `POST`, `PUT`,
`PATCH`, `DELETE` or `OPTIONS`; any other method counts as `OTHER`. `status` is the numeric status
as a string.

**Histogram buckets**, in seconds, plus `+Inf`:

`0.001`, `0.0025`, `0.005`, `0.01`, `0.025`, `0.05`, `0.1`, `0.25`, `0.5`, `1`, `2.5`, `5`

A sample:

```
# HELP deedglass_build_info The version and commit of this build.
# TYPE deedglass_build_info gauge
deedglass_build_info{version="0.1.0-dev",commit="5495f7e"} 1
# HELP deedglass_http_in_flight Requests being served now.
# TYPE deedglass_http_in_flight gauge
deedglass_http_in_flight 0
# HELP deedglass_http_requests_total HTTP requests by route pattern, method and status.
# TYPE deedglass_http_requests_total counter
deedglass_http_requests_total{route="/v1/health",method="GET",status="200"} 42
deedglass_http_requests_total{route="unmatched",method="GET",status="404"} 1
# HELP deedglass_http_request_duration_seconds Time to serve a request, by route pattern.
# TYPE deedglass_http_request_duration_seconds histogram
deedglass_http_request_duration_seconds_bucket{route="/v1/health",le="0.001"} 40
…
deedglass_http_request_duration_seconds_bucket{route="/v1/health",le="+Inf"} 42
deedglass_http_request_duration_seconds_sum{route="/v1/health"} 0.031
deedglass_http_request_duration_seconds_count{route="/v1/health"} 42
```

## Prometheus scrape config

```yaml
scrape_configs:
  - job_name: deedglass
    metrics_path: /metrics
    scrape_interval: 15s
    static_configs:
      - targets: ["127.0.0.1:9464"]
```

Two queries to start with:

```promql
# p99 latency by route, over 5 minutes
histogram_quantile(0.99, sum by (route, le) (rate(deedglass_http_request_duration_seconds_bucket[5m])))

# share of 5xx answers, over 5 minutes
sum(rate(deedglass_http_requests_total{status=~"5.."}[5m])) / sum(rate(deedglass_http_requests_total[5m]))
```

## What this doesn't do

- **No tracing yet.** There are no spans and no trace headers. Correlate with `request_id`, and
  send your own id in `X-Request-Id` to see it as `client_request_id`.
- **No per-org metrics labels.** Metrics carry no org, agent or key, so their size stays fixed as
  orgs grow. Per-org detail is in the access log (`org_id`, `agent_id`).
- **No request bodies in logs.** Not at `debug` either. Neither are response bodies or query
  strings.
- **No authentication on the metrics listener.** Keep it on localhost or a private network.
- **No log shipping.** Logs go to the process's output; collect them with your platform's tools.
