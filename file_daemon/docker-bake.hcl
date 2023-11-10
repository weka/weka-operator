variable "version" {
    default = "0.0.1"
}

target "default" {
    dockerfile = "./Dockerfile"
    tags = [
        "kind-registry:5001/file-daemon:${version}",
        "file-daemon:${version}"
        ]
}
