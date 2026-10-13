FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.2@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION

# git is required to fetch go dependencies
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git openssh-client
ENV GOPRIVATE=github.com/weka

# Credentials for private module fetch are mounted as build secrets on the go steps
# below, so they never enter an image layer or the exported build cache.
RUN mkdir -p -m 0700 ~/.ssh && ssh-keyscan github.com >> ~/.ssh/known_hosts

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
COPY pkg/weka-k8s-api/go.mod pkg/weka-k8s-api/go.mod
COPY pkg/weka-k8s-api/go.sum pkg/weka-k8s-api/go.sum
COPY pkg/go-steps-engine/go.mod pkg/go-steps-engine/go.mod
COPY pkg/go-steps-engine/go.sum pkg/go-steps-engine/go.sum
RUN --mount=type=secret,id=gitconfig,target=/root/.gitconfig,required=false \
  --mount=type=secret,id=netrc,target=/root/.netrc,required=false \
  --mount=type=ssh --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
  go mod download

COPY ./ /workspace

RUN --mount=type=secret,id=gitconfig,target=/root/.gitconfig,required=false \
    --mount=type=secret,id=netrc,target=/root/.netrc,required=false \
    --mount=type=ssh \
    --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build,id=gobuild-$TARGETARCH \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/weka/weka-operator/internal/runtime/config.version=${VERSION}" \
    -o /dist/weka-pod-runtime ./cmd/weka-pod-runtime

# busybox (not scratch): the operator injects this image as an init container
# that runs `cp /weka-pod-runtime ...`, so the image must provide `cp`.
FROM quay.io/weka.io/busybox:1.37.0@sha256:b3255e7dfbcd10cb367af0d409747d511aeb66dfac98cf30e97e87e4207dd76f
COPY --from=builder /dist/weka-pod-runtime /weka-pod-runtime
ENTRYPOINT ["/weka-pod-runtime"]
