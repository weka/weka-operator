FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.0@sha256:4013ae0f9e7994f8535c58c811f8f863fbed38b72e0d51e6592156f758d66146 as builder
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

FROM registry.access.redhat.com/ubi9/ubi:9.8@sha256:9295c5c688f487fa5cf27a734fa55ecd57aeb7dc0904ba537da4f42dfa1d0acb as final
COPY --from=builder /dist/weka-operator /weka-operator
COPY --from=builder /dist/weka-capacity /weka-capacity
ENTRYPOINT ["/weka-operator"]
