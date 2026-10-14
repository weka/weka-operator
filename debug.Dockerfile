# Operator binary built with -gcflags="all=-N -l" (bin/weka-operator-debug) plus Delve.
# Built by script/deploy-debug.sh with ./bin as the build context.
FROM docker.io/library/golang:1.27.0@sha256:4013ae0f9e7994f8535c58c811f8f863fbed38b72e0d51e6592156f758d66146 AS delve-builder
RUN CGO_ENABLED=0 go install -ldflags "-s -w" github.com/go-delve/delve/cmd/dlv@v1.27.2

FROM registry.access.redhat.com/ubi9/ubi:9.8@sha256:9295c5c688f487fa5cf27a734fa55ecd57aeb7dc0904ba537da4f42dfa1d0acb
COPY --chmod=755 weka-operator-debug /weka-operator
COPY --from=delve-builder --chmod=755 /go/bin/dlv /usr/local/bin/dlv
ENTRYPOINT ["/weka-operator"]
