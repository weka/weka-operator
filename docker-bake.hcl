variable "VERSION" {
  default = "latest"
}

# Build the docker images for the operator's various binaries
group "default" {
  targets = ["manager", "device-plugin", "node-labeller"]
}

# The manager is the name of the main operator binary
target "manager" {
  platforms = ["linux/amd64", "linux/arm64"]
  dockerfile = "Dockerfile"
  tags = ["quay.io/weka.io/weka-operator:${VERSION}"]
  target = "manager"
}

# device plugin
target "device-plugin" {
  platforms = ["linux/amd64", "linux/arm64"]
  dockerfile = "Dockerfile"
  tags = ["quay.io/weka.io/weka-operator-device-plugin:${VERSION}"]
  target = "device-plugin"
}

# node-labeller
target "node-labeller" {
  platforms = ["linux/amd64", "linux/arm64"]
  dockerfile = "Dockerfile"
  tags = ["quay.io/weka.io/weka-operator-node-labeller:${VERSION}"]
  target = "node-labeller"
}
