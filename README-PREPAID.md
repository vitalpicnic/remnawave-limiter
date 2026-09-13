# Prepaid traffic module for syvlech/remnawave-limiter

This overlay adds a second binary, `remnawave-prepaid-limiter`, without changing the upstream device/HWID limiter source.

Copy into the root of your fork preserving paths:

- `internal/api/traffic.go`
- `internal/prepaid/config.go`
- `internal/prepaid/store.go`
- `internal/prepaid/service.go`
- `internal/prepaid/webhook.go`
- `internal/prepaid/service_test.go`
- `internal/prepaid/webhook_test.go`
- `cmd/prepaid-limiter/main.go`
- replace root `Dockerfile` with the one in this overlay
- use `.env.prepaid.example` as the server `.env` template
- use `docker-compose.prepaid.yml` on the server (replace YOUR_GITHUB_LOGIN)

Operational policy:

1. Paid/LIMITED node user consumption multiplier = 1.
2. Unlimited node user consumption multiplier = 0.
3. Users start with both Internal Squads and `trafficLimitStrategy=NO_RESET`.
4. On `user.limited`, the service stores Redis state, removes only the limited squad, ensures unlimited squad, sets `trafficLimitBytes=0`, `NO_RESET`, and `ACTIVE`.
5. Reset Traffic alone while blocked does not restore paid access.
6. A new package is activated only when the blocked user has `trafficLimitBytes > 0` and `usedTrafficBytes == 0`.
7. Recommended admin order after payment: Reset Traffic -> set new Data Limit.
8. The reconciler also scans Remnawave users with status LIMITED, so a missed `user.limited` webhook is repaired.

Before merge/publish, run in the actual fork:

```bash
gofmt -w internal/api/traffic.go internal/prepaid cmd/prepaid-limiter
go test ./...
docker build -t remnawave-limiter:prepaid-test .
```

The code in this overlay was syntax-formatted with `gofmt`, but full `go test ./...` could not be run in the ChatGPT container because the upstream repository/dependencies are not available there.
