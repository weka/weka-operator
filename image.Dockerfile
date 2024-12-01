ARG UBI_HASH=9ac75c1a392429b4a087971cdf9190ec42a854a169b6835bc9e25eecaf851258
FROM registry.access.redhat.com/ubi9/ubi@sha256:${UBI_HASH}
ADD dist/weka-operator /weka-operator
ENTRYPOINT ["/weka-operator"]