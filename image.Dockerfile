FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b as builder
ARG TARGETOS
ARG TARGETARCH

# git is required to fetch go dependencies
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git openssh-client
ENV GOPRIVATE=github.com/weka

# Credentials for private module fetch are mounted as build secrets on the go steps
# below. Secrets live in a tmpfs for the duration of a RUN, so unlike a copied file they
# never enter an image layer and are never published by --cache-to type=gha,mode=max.
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
RUN --mount=type=secret,id=gitconfig,target=/root/.gitconfig,required=false \
  --mount=type=secret,id=netrc,target=/root/.netrc,required=false \
  --mount=type=ssh --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
  go mod download

COPY ./ /workspace

RUN --mount=type=secret,id=gitconfig,target=/root/.gitconfig,required=false \
    --mount=type=secret,id=netrc,target=/root/.netrc,required=false \
    --mount=type=ssh \
    --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build,id=gobuild-$TARGETARCH \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /dist/weka-operator cmd/manager/main.go

# weka-capacity is the capacity-planner dry-run CLI. Built by package path (multi-file main) and shipped
# alongside the operator so it can be invoked via `kubectl exec ... -- /weka-capacity ...`.
# Stripped (-s -w) and trimmed (-trimpath): this binary ships only to be exec'd for a dry-run
# preview, so debug symbols and build paths aren't needed and dropping them shrinks the image.
RUN --mount=type=secret,id=gitconfig,target=/root/.gitconfig,required=false \
    --mount=type=secret,id=netrc,target=/root/.netrc,required=false \
    --mount=type=ssh \
    --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build,id=gobuild-$TARGETARCH \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-s -w" -trimpath -o /dist/weka-capacity ./cmd/weka-capacity

FROM registry.access.redhat.com/ubi9/ubi:9.8@sha256:8f989cc9270da6abec0144c22846ed1e6e92e462dc7768ce0981c4ac1d574733 as final
COPY --from=builder /dist/weka-operator /weka-operator
COPY --from=builder /dist/weka-capacity /weka-capacity
ENTRYPOINT ["/weka-operator"]
