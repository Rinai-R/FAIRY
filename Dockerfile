# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY fairy/go.mod fairy/go.sum ./
RUN go mod download
COPY fairy/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fairy .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates \
  && mkdir -p /data
WORKDIR /data
ENV FAIRY_CONFIG_ROOT=/data
ENV FAIRY_LISTEN_ADDR=0.0.0.0:8787
COPY --from=build /out/fairy /usr/local/bin/fairy
EXPOSE 8787
ENTRYPOINT ["/usr/local/bin/fairy"]
