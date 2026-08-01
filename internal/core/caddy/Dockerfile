# OwnBase-hardened Caddy.
#
# The official caddy:2.11.4-alpine image still ships:
#   - alpine packages (curl, c-ares) with published fixes apk can apply
#   - a caddy binary built with go1.26.3 that has fixable stdlib / x/text / grpc CVEs
#
# We rebuild the binary from the same Caddy tag with a current Go toolchain and
# start from a fresh alpine with apk upgrade, so both layers clear.

ARG CADDY_VERSION=v2.11.4
ARG GO_IMAGE=docker.io/library/golang:1.26.5-alpine

FROM ${GO_IMAGE} AS build
ARG CADDY_VERSION
RUN apk add --no-cache git ca-certificates
WORKDIR /src
RUN git clone --depth 1 --branch "${CADDY_VERSION}" https://github.com/caddyserver/caddy.git .
# Building with Go 1.26.5 clears stdlib CVEs in the caddy binary. Bump
# known-vulnerable module deps to patched releases the go.mod permits.
WORKDIR /src
RUN go get golang.org/x/text@v0.39.0 \
    && go get google.golang.org/grpc@v1.82.1 \
    && go mod tidy
WORKDIR /src/cmd/caddy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /usr/bin/caddy

FROM docker.io/library/alpine:3.23
RUN apk upgrade --no-cache \
    && apk add --no-cache \
        ca-certificates \
        curl \
        libcap \
        mailcap

RUN set -eux; \
    mkdir -p \
        /config/caddy \
        /data/caddy \
        /etc/caddy \
        /usr/share/caddy \
    ; \
    chmod 1777 /config/caddy /data/caddy

# Default Caddyfile + welcome page (same assets the official image ships).
ADD https://github.com/caddyserver/dist/raw/33ae08ff08d168572df2956ed14fbc4949880d94/config/Caddyfile /etc/caddy/Caddyfile
ADD https://github.com/caddyserver/dist/raw/33ae08ff08d168572df2956ed14fbc4949880d94/welcome/index.html /usr/share/caddy/index.html

COPY --from=build /usr/bin/caddy /usr/bin/caddy
RUN setcap cap_net_bind_service=+ep /usr/bin/caddy \
    && chmod +x /usr/bin/caddy \
    && caddy version

ENV XDG_CONFIG_HOME=/config
ENV XDG_DATA_HOME=/data

EXPOSE 80
EXPOSE 443
EXPOSE 443/udp
EXPOSE 2019

WORKDIR /srv

CMD ["caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]
