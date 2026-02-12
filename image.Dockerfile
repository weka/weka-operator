FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.24.3 as builder
ARG TARGETOS
ARG TARGETARCH

# git is required to fetch go dependencies
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git openssh-client
ENV GOPRIVATE=github.com/weka

COPY dockerfile_files /root/
RUN mkdir -p -m 0700 ~/.ssh && ssh-keyscan github.com >> ~/.ssh/known_hosts

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
COPY pkg/weka-k8s-api/go.mod pkg/weka-k8s-api/go.mod
COPY pkg/weka-k8s-api/go.sum pkg/weka-k8s-api/go.sum
COPY pkg/go-steps-engine/go.mod pkg/go-steps-engine/go.mod
COPY pkg/go-steps-engine/go.sum pkg/go-steps-engine/go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN --mount=type=ssh --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
  go mod download

COPY ./ /workspace

RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build,id=gobuild-$TARGETARCH \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /dist/weka-operator cmd/manager/main.go

FROM registry.access.redhat.com/ubi9/ubi as final
COPY --from=builder /dist/weka-operator /weka-operator
ENTRYPOINT ["/weka-operator"]
