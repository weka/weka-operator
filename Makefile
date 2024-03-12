# CHANNELS define the bundle channels used in the bundle.
# Add a new line here if you would like to change its default config. (E.g CHANNELS = "candidate,fast,stable")
# To re-generate a bundle for other specific channels without changing the standard setup, you can:
# - use the CHANNELS as arg of the bundle target (e.g make bundle CHANNELS=candidate,fast,stable)
# - use environment variables to overwrite this value (e.g export CHANNELS="candidate,fast,stable")
ifneq ($(origin CHANNELS), undefined)
BUNDLE_CHANNELS := --channels=$(CHANNELS)
endif

# DEFAULT_CHANNEL defines the default channel used in the bundle.
# Add a new line here if you would like to change its default config. (E.g DEFAULT_CHANNEL = "stable")
# To re-generate a bundle for any other default channel without changing the default setup, you can:
# - use the DEFAULT_CHANNEL as arg of the bundle target (e.g make bundle DEFAULT_CHANNEL=stable)
# - use environment variables to overwrite this value (e.g export DEFAULT_CHANNEL="stable")
ifneq ($(origin DEFAULT_CHANNEL), undefined)
BUNDLE_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)
endif
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

# IMAGE_TAG_BASE defines the docker.io namespace and part of the image name for remote images.
# This variable is used to construct full image tags for bundle and catalog images.
#
# For example, running 'make bundle-build bundle-push catalog-build catalog-push' will build and push both
# weka.io/weka-operator-bundle:$VERSION and weka.io/weka-operator-catalog:$VERSION.
#IMAGE_TAG_BASE ?= weka.io/weka-operator
IMAGE_TAG_BASE ?= quay.io/weka.io/weka-operator

# Set the Operator SDK version to use. By default, what is installed on the system is used.
# This is useful for CI or a project to utilize a specific version of the operator-sdk toolkit.
OPERATOR_SDK_VERSION ?= v1.32.0

# Image URL to use all building/pushing image targets
IMG ?= $(IMAGE_TAG_BASE):$(VERSION)
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.26.0

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

CRD = charts/weka-operator/crds/weka.weka.io_clients.yaml
CRD_TYPES = internal/pkg/api/v1alpha1/client_types.go \
		internal/pkg/api/v1alpha1/cluster_types.go \
		internal/pkg/api/v1alpha1/backend_types.go \
		internal/pkg/api/v1alpha1/drive_types.go \
		internal/pkg/api/v1alpha1/container_types.go 

$(CRD): controller-gen $(CRD_TYPES)

.PHONY: crd
crd: $(CRD) ## Generate CustomResourceDefinition objects.
	mkdir -p charts/weka-operator/crds
	$(CONTROLLER_GEN) crd paths="./..." output:crd:artifacts:config=charts/weka-operator/crds

RBAC = charts/weka-operator/templates/role.yaml
$(RBAC): controller-gen internal/app/manager/controllers/client_controller.go

.PHONY: rbac
rbac: $(RBAC) ## Generate RBAC objects.
	mkdir -p charts/weka-operator/templates
	$(CONTROLLER_GEN) rbac:roleName=weka-operator-manager-role paths="./..." output:rbac:artifacts:config=charts/weka-operator/templates

.PHONY: manifests
manifests: controller-gen crd rbac ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: envtest## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -i --bin-dir $(LOCALBIN) -p path)" go test -v ./... -coverprofile cover.out

CLUSTER_SAMPLE=config/samples/weka_v1alpha1_cluster.yaml
.PHONY: cluster-sample
cluster-sample: ## Deploy sample cluster CRD
	kubectl apply -f $(CLUSTER_SAMPLE)

##@ Build

.PHONY: build
build: ## Build manager binary.
	goreleaser release --snapshot --clean --config .goreleaser.dev.yaml

.PHONY: dev
dev:
	$(MAKE) build
	$(MAKE) docker-push
	- $(MAKE) undeploy
	$(MAKE) deploy VERSION=latest

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/manager/main.go

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMAGE_TAG_BASE}:latest
	docker push ${IMAGE_TAG_BASE}-node-labeller:latest

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	kubectl apply -f charts/weka-operator/crds

.PHONY: uninstall
uninstall: manifests ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	kubectl delete --ignore-not-found=$(ignore-not-found) -f charts/weka-operator/crds

NAMESPACE="weka-operator-system"
VALUES="prefix=weka-operator,image.repository=$(IMAGE_TAG_BASE),image.tag=$(VERSION)"
HELM_TEMPLATE=$(HELM) --namespace $(NAMESPACE) \
	template weka-operator charts/weka-operator \
	--values charts/weka-operator/values.yaml \
	--set $(VALUES)

.PHONY: deploy
deploy: ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	$(HELM) upgrade --install weka-operator charts/weka-operator \
		--namespace $(NAMESPACE) \
		--values charts/weka-operator/values.yaml \
		--set $(VALUES)

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(HELM) uninstall weka-operator --namespace $(NAMESPACE)

##@ Helm Chart
HELM=helm
CHART=charts/weka-operator
CHART_ARCHIVE=charts/weka-operator-$(VERSION).tgz

.PHONY: chart
chart: $(CHART_ARCHIVE) ## Build Helm chart.
	$(HELM) lint $(CHART)
	$(HELM) package $(CHART) --destination charts --version $(VERSION)

$(CHART_ARCHIVE): $(CRD)

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest

## Tool Versions
CONTROLLER_TOOLS_VERSION ?= v0.11.1

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary. If wrong version is installed, it will be overwritten.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen && $(LOCALBIN)/controller-gen --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: operator-sdk
OPERATOR_SDK ?= $(LOCALBIN)/operator-sdk
operator-sdk: ## Download operator-sdk locally if necessary.
ifeq (,$(wildcard $(OPERATOR_SDK)))
ifeq (, $(shell which operator-sdk 2>/dev/null))
	@{ \
	set -e ;\
	mkdir -p $(dir $(OPERATOR_SDK)) ;\
	OS=$(shell go env GOOS) && ARCH=$(shell go env GOARCH) && \
	curl -sSLo $(OPERATOR_SDK) https://github.com/operator-framework/operator-sdk/releases/download/$(OPERATOR_SDK_VERSION)/operator-sdk_$${OS}_$${ARCH} ;\
	chmod +x $(OPERATOR_SDK) ;\
	}
else
OPERATOR_SDK = $(shell which operator-sdk)
endif
endif

