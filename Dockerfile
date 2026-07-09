# syntax=docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89
FROM golang:1.26.5@sha256:63f132d58c1f589f0dcda584933a9bb44bfda1150f1506377f5a902f34d86033 AS prerequisites

ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=bind,source=go.sum,target=go.sum \
    --mount=type=bind,source=go.mod,target=go.mod \
    go mod download

FROM prerequisites AS build

ARG VERSION=dev

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=bind,target=. \
    CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/caravan ./cmd/caravan

FROM gcr.io/distroless/base-debian13@sha256:7c4468db5fea18a1630860619be640c4c0ad158c0d63f12951b96b7d0f5ddd62 AS release

COPY --from=build /out/caravan /usr/local/bin/caravan

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/caravan"]
