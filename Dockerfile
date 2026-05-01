FROM golang:1.25-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o liga-discord-gc .

FROM alpine:3.20

WORKDIR /app

COPY --from=builder /build/liga-discord-gc .

EXPOSE 8080

CMD ["./liga-discord-gc"]
