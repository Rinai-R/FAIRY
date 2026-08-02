# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS build
WORKDIR /src

COPY fairy/go.mod fairy/go.sum ./fairy/
COPY surfaces/qq-onebot/go.mod surfaces/qq-onebot/go.sum ./surfaces/qq-onebot/
RUN cd surfaces/qq-onebot && go mod download

COPY fairy/ ./fairy/
COPY surfaces/qq-onebot/ ./surfaces/qq-onebot/
RUN cd surfaces/qq-onebot \
  && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fairy-qq-onebot .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates \
  && addgroup -S fairy \
  && adduser -S -G fairy fairy
COPY --from=build /out/fairy-qq-onebot /usr/local/bin/fairy-qq-onebot
USER fairy
EXPOSE 3002
ENTRYPOINT ["/usr/local/bin/fairy-qq-onebot"]
CMD ["serve"]

