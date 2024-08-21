FROM gcr.io/distroless/static:nonroot
ADD dist/weka-operator /weka-operator
ENTRYPOINT ["/weka-operator"]