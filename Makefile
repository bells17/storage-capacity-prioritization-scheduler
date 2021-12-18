BINDIR := $(shell pwd)/bin
DEEPCOPY_GEN := $(BINDIR)/deepcopy-gen
DEEPCOPY_GEN_VERSION ?= 0.22.2

KUBERNETES_VERSION = 1.23.3
KUBECTL := $(BINDIR)/kubectl
KUBECONFIG := $(shell pwd)/.kubeconfig
export KUBECTL KUBECONFIG

HELM_VERSION = 3.7.1
HELM := $(BINDIR)/helm

.PHONY: init
init:
	go mod download
	go mod vendor
	patch vendor/k8s.io/kubernetes/pkg/scheduler/framework/plugins/volumebinding/volume_binding.go volume_binding.patch

.PHONY: test
test:
	go test -race -v -count 1 ./... --timeout=60s

.PHONY: build
build:
	docker build --no-cache -t storage-capacity-prioritization-scheduler:devel .

.PHONY: setup
setup: $(KUBECTL) $(HELM)

.PHONY: install
install:
	$(HELM) install storage-capacity-prioritization-scheduler \
		charts/storage-capacity-prioritization-scheduler \
		--create-namespace \
		--namespace storage-capacity-prioritization-scheduler \
		-f values.yaml

.PHONY: uninstall
uninstall:
	$(HELM) uninstall storage-capacity-prioritization-scheduler --namespace storage-capacity-prioritization-scheduler

.PHONY: generate
generate: $(DEEPCOPY_GEN)
	cd $(shell pwd) && \
	$(DEEPCOPY_GEN) \
		--input-dirs ./pkg/apis/config \
		--output-file-base zz_generated.deepcopy \
		--output-base $(shell pwd)/../../../ \
		--go-header-file ./boilerplate.txt

$(BINDIR):
	mkdir $@

$(DEEPCOPY_GEN): $(BINDIR)
	$(call go-get-tool,$(DEEPCOPY_GEN),k8s.io/code-generator/cmd/deepcopy-gen@v$(DEEPCOPY_GEN_VERSION))

$(KUBECTL): $(BINDIR)
	curl -sfL -o $@ https://dl.k8s.io/release/v$(KUBERNETES_VERSION)/bin/linux/amd64/kubectl
	chmod a+x $@

$(HELM): $(BINDIR)
	curl -L -sS https://get.helm.sh/helm-v$(HELM_VERSION)-linux-amd64.tar.gz \
	  | tar xz -C $(BINDIR) --strip-components 1 linux-amd64/helm

# go-get-tool will 'go get' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-get-tool
@[ -f $(1) ] || { \
set -e ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go install $(2) ;\
}
endef
