FROM gcr.io/distroless/static:nonroot
ADD dist/weka-operator /weka-operator
ENTRYPOINT ["/weka-operator"]
USER 65532:65532