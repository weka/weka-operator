# Operator binary built with -gcflags="all=-N -l" (bin/weka-operator-debug) plus Delve.
# Built by script/deploy-debug.sh with ./bin as the build context.
FROM docker.io/library/golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e AS delve-builder
RUN CGO_ENABLED=0 go install -ldflags "-s -w" github.com/go-delve/delve/cmd/dlv@v1.27.2

FROM registry.access.redhat.com/ubi9/ubi:9.9@sha256:6636b79fba9e5044360366f189333c30b5ce528efd92c40c68b074592b971fcd
COPY --chmod=755 weka-operator-debug /weka-operator
COPY --from=delve-builder --chmod=755 /go/bin/dlv /usr/local/bin/dlv
ENTRYPOINT ["/weka-operator"]
