.PHONY: all
all: build

.PHONY: build
build: ## Build packer image
	packer build packer-template.pkr.hcl

.PHONY: test
test: ## Run packer tests
	packer validate packer-template.pkr.hcl
