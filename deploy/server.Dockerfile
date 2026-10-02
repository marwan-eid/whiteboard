# syntax=docker/dockerfile:1
# The build stage runs on the build machine and cross-compiles, so arm64
# images (Oracle A1) build fast on amd64 runners.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/whiteboard ./cmd/whiteboard

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/whiteboard /whiteboard
EXPOSE 8081
ENTRYPOINT ["/whiteboard"]
