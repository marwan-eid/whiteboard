# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/whiteboard ./cmd/whiteboard

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/whiteboard /whiteboard
EXPOSE 8081
ENTRYPOINT ["/whiteboard"]
