FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /home-presence .

FROM alpine:3.21

RUN apk add --no-cache net-tools samba-client

COPY --from=builder /home-presence /usr/local/bin/home-presence

VOLUME /data
EXPOSE 8080

ENTRYPOINT ["home-presence"]
CMD ["-role", "both", "-home", "家庭A", "-port", "8080"]
