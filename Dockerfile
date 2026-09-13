FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Do not publish an image if the fork does not pass its test suite.
RUN go test ./...

# Keep the original syvlech binary in the image for rollback/debugging.
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/remnawave-limiter ./cmd/limiter/

# Our prepaid traffic binary is the default entrypoint.
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/remnawave-prepaid-limiter ./cmd/prepaid-limiter/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /bin/remnawave-limiter /usr/local/bin/remnawave-limiter
COPY --from=builder /bin/remnawave-prepaid-limiter /usr/local/bin/remnawave-prepaid-limiter

WORKDIR /app
ENTRYPOINT ["remnawave-prepaid-limiter"]
