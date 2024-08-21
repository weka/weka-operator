# Build the manager binary
FROM golang:1.22 as builder
# right now this image is not in use, might be in future when whole release process runs as docker build
# and when used probably will be heavily re-done

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
  go mod download

# Copy the go source
COPY cmd/ cmd/
COPY hack/ hack/
# Is it really needed?
COPY config/ config/
COPY charts/ charts/
COPY Makefile Makefile
COPY pkg/ pkg/
COPY internal/ internal/

FROM builder as build-manager

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -a -o manager cmd/manager/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot as manager
WORKDIR /
COPY --from=build-manager /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
