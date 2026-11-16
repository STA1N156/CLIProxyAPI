FROM golang:1.27.4-bookworm AS builder

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends build-essential git && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN TLS_OVERLAY="$(go run ./cmd/antigravity_tls_overlay)" && CGO_ENABLED=1 GOOS=linux go build -overlay="$TLS_OVERLAY" -buildvcs=false -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./CLIProxyAPI ./cmd/server/

FROM debian:bookworm

ARG TARGETARCH
ENV AGY_VERSION=1.5.9
RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates curl && \
    case "${TARGETARCH:-$(dpkg --print-architecture)}" in \
      amd64) AGY_ARCH=x64; AGY_SHA256=8b1f5e56779f2434a0af36f1b715a8392a084de550f8c68bc79821f95fabf3fa ;; \
      arm64) AGY_ARCH=arm64; AGY_SHA256=3a18c238c7ee6661e43df7e824a7d4c1bed78aeeda09430921a0f1dbfe825bca ;; \
      *) exit 1 ;; \
    esac && \
    curl -fL "https://github.com/google-antigravity/antigravity-cli/releases/download/${AGY_VERSION}/agy_cli_linux_${AGY_ARCH}.tar.gz" -o /tmp/agy.tar.gz && \
    echo "${AGY_SHA256}  /tmp/agy.tar.gz" | sha256sum -c - && \
    mkdir /tmp/agy && tar -xzf /tmp/agy.tar.gz -C /tmp/agy && \
    install "$(find /tmp/agy -type f -name antigravity -print -quit)" /usr/local/bin/agy && \
    rm -rf /tmp/agy /tmp/agy.tar.gz /var/lib/apt/lists/*

RUN mkdir /CLIProxyAPI

COPY --from=builder ./app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI

COPY config.example.yaml /CLIProxyAPI/config.example.yaml

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./CLIProxyAPI"]
