# syntax=docker/dockerfile:1.7
# Go version is pinned here and in go.mod (go 1.27.1).
FROM golang:1.27.1-alpine3.24 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
COPY api ./api
ARG BUILD_TAGS=""
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -tags "${BUILD_TAGS}" -ldflags="-s -w" -o /out/jungle ./cmd/jungle

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/jungle /jungle
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/jungle"]
CMD ["serve"]
