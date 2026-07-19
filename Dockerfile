FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /rollout-monitor ./cmd/monitor
RUN CGO_ENABLED=0 GOOS=linux go build -o /rollout-dispatcher ./cmd/dispatcher

FROM alpine:3.20
RUN apk --no-cache add ca-certificates
COPY --from=builder /rollout-monitor /usr/local/bin/rollout-monitor
COPY --from=builder /rollout-dispatcher /usr/local/bin/rollout-dispatcher
ENTRYPOINT ["rollout-monitor"]
