FROM golang:1.21-alpine AS builder

WORKDIR /app

COPY go.mod ./
RUN go mod download -x && go mod graph

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o stackmgr-proxy .

FROM alpine:latest

RUN apk --no-cache add ca-certificates docker-cli docker-compose

WORKDIR /root/

COPY --from=builder /app/stackmgr-proxy .

EXPOSE 8080

CMD ["./stackmgr-proxy"]
