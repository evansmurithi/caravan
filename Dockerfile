# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183
FROM golang:1.27.4@sha256:5f1b98fa02fe396ebce2a0227a0e08309e11792915b9dd2e1f71053d7e7e6666 AS prerequisites

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

FROM gcr.io/distroless/base-debian13@sha256:1eb7e763df3f75a0d0eab87368f04e1bc61ce268caf3f2cbae765fa8fd4fd3e8 AS release

COPY --from=build /out/caravan /usr/local/bin/caravan

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/caravan"]
