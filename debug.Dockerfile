# Operator binary built with -gcflags="all=-N -l" (bin/weka-operator-debug) plus Delve.
# Built by script/deploy-debug.sh with ./bin as the build context.
FROM docker.io/library/golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS delve-builder
RUN CGO_ENABLED=0 go install -ldflags "-s -w" github.com/go-delve/delve/cmd/dlv@v1.27.2

FROM registry.access.redhat.com/ubi9/ubi:9.8@sha256:8f989cc9270da6abec0144c22846ed1e6e92e462dc7768ce0981c4ac1d574733
COPY --chmod=755 weka-operator-debug /weka-operator
COPY --from=delve-builder --chmod=755 /go/bin/dlv /usr/local/bin/dlv
ENTRYPOINT ["/weka-operator"]
