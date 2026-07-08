# syntax=docker/dockerfile:1

FROM golang:1.26 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/caravan ./cmd/caravan

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/caravan /usr/local/bin/caravan
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/caravan"]
