FROM docker.io/library/golang:1.27.0 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=ssh go mod download
COPY . .
RUN --mount=type=ssh \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /dist/weka-pod-runtime ./cmd/weka-pod-runtime/main.go

# busybox (not scratch): the operator injects this image as an init container
# that runs `cp /weka-pod-runtime ...`, so the image must provide `cp`.
FROM busybox:latest
COPY --from=builder /dist/weka-pod-runtime /weka-pod-runtime
ENTRYPOINT ["/weka-pod-runtime"]
