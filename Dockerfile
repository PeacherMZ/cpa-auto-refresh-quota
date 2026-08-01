# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS builder

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETARCH=amd64
ARG VERSION=""

RUN mkdir -p /out \
    && version="${VERSION#v}" \
    && artifact="cpa-auto-refresh-quota.so" \
    && ldflags="-s -w" \
    && if [ -n "${version}" ]; then artifact="cpa-auto-refresh-quota-v${version}.so"; ldflags="${ldflags} -X main.pluginVersion=${version}"; fi \
    && CGO_ENABLED=1 GOOS=linux GOARCH="${TARGETARCH}" \
       go build -buildvcs=false -trimpath -ldflags="${ldflags}" \
       -buildmode=c-shared \
       -o "/out/${artifact}" \
       ./cmd/cpa-auto-refresh-quota \
    && rm -f /out/*.h

FROM scratch AS artifact

COPY --from=builder /out/ /
