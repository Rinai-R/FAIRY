# syntax=docker/dockerfile:1

FROM node:22-bookworm AS web
WORKDIR /src
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY web/package.json web/
RUN corepack enable && corepack prepare pnpm@11.10.0 --activate \
  && pnpm install --frozen-lockfile --filter @fairy/web...
COPY web/ web/
RUN pnpm --filter @fairy/web build

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY fairy/go.mod fairy/go.sum ./
RUN go mod download
COPY fairy/ ./
COPY --from=web /src/fairy/api/console/dist ./api/console/dist
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
CMD ["serve"]
