# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
FROM golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS prerequisites

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

FROM gcr.io/distroless/base-debian13@sha256:f5e95195e998742ff590b7569bae479accce1226e51ae6c3118954030121cdcb AS release

COPY --from=build /out/caravan /usr/local/bin/caravan

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/caravan"]
