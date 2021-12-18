KUBERNETES_VERSION := 1.23.3
TOPOLVM_VERSION := 0.10.5
CERT_MANAGER_VERSION := 1.7.0
export KUBERNETES_VERSION

PREFIX := scp-scheduler
BACKING_STORE := ./lvmd
GINKGO_VERSION := $(shell awk '/github.com\/onsi\/ginkgo\/v2/ {print substr($$2, 2)}' ../go.mod)

KIND_NODE_IMAGE=kindest/node:v$(KUBERNETES_VERSION)
KIND_CLUSTER_NAME := $(PREFIX)-e2e
KUBECONFIG := $(shell pwd)/.kubeconfig
export KUBECTL KUBECONFIG

SUDO := sudo
BINDIR := $(abspath $(shell pwd)/../bin)
TMPDIR := /tmp/$(PREFIX)
KUBECTL := $(BINDIR)/kubectl
HELM := $(BINDIR)/helm
GINKGO := $(BINDIR)/ginkgo
KIND := $(TMPDIR)/topolvm/bin/kind


.PHONY: setup
setup:
	$(MAKE) -C ../ setup
	GOBIN=$(BINDIR) go install github.com/onsi/ginkgo/v2/ginkgo@v$(GINKGO_VERSION)

.PHONY: test
test:
	E2ETEST=1 BINDIR=$(BINDIR) $(GINKGO) --failFast -v .

$(TMPDIR)/scp-scheduler.img:
	$(MAKE) -C ../ build
	docker save -o $@ storage-capacity-prioritization-scheduler:devel

$(TMPDIR)/scp-scheduler-cluster.yaml:
	sed -e "s|@KUBERNETES_VERSION@|$(KUBERNETES_VERSION)|" scp-scheduler-cluster.yaml > $@

.PHONY: launch-kind
launch-kind: start-lvmd $(TMPDIR)/scp-scheduler-cluster.yaml $(TMPDIR)/scp-scheduler.img
	$(KIND) create cluster \
		--name=$(KIND_CLUSTER_NAME) \
		--image=$(KIND_NODE_IMAGE) \
		--config=$(TMPDIR)/scp-scheduler-cluster.yaml
	$(KIND) load image-archive --name=$(KIND_CLUSTER_NAME) $(TMPDIR)/scp-scheduler.img
	$(MAKE) install-topolvm
	$(MAKE) -C ../ install KUBECONFIG=$(KUBECONFIG)

.PHONY: reload-image
reload-image:
	rm $(TMPDIR)/scp-scheduler.img
	make $(TMPDIR)/scp-scheduler.img
	$(KIND) load image-archive --name=$(KIND_CLUSTER_NAME) $(TMPDIR)/scp-scheduler.img

.PHONY: install-topolvm
install-topolvm:
	$(KUBECTL) apply -f https://github.com/cert-manager/cert-manager/releases/download/v$(CERT_MANAGER_VERSION)/cert-manager.yaml
	$(KUBECTL) wait --for=condition=available --timeout=180s -n=cert-manager deployments/cert-manager-webhook
	$(KUBECTL) create namespace topolvm-system || true
	$(KUBECTL) label namespace topolvm-system topolvm.cybozu.com/webhook=ignore || true
	$(HELM) repo add jetstack https://charts.jetstack.io
	$(HELM) repo update
	$(HELM) dependency build $(TMPDIR)/topolvm/charts/topolvm/
	$(HELM) install --namespace=topolvm-system topolvm $(TMPDIR)/topolvm/charts/topolvm/ -f topolvm-values.yaml
	$(KUBECTL) wait --for=condition=available --timeout=180s -n=topolvm-system deployments/topolvm-controller

.PHONY: shutdown-kind
shutdown-kind:
	$(KIND) delete cluster --name=$(KIND_CLUSTER_NAME) || true
	sleep 2
	for d in $$($(SUDO) find $(TMPDIR) -type d); do \
		if $(SUDO) mountpoint -q $$d; then \
			$(SUDO) umount $$d; \
		fi; \
	done
	for d in $$(mount | grep /lib/kubelet | cut -d ' ' -f 3); do $(SUDO) umount $$d; done

$(BINDIR)/lvmd: $(TMPDIR)/topolvm
	cd $(TMPDIR)/topolvm && go build -o $(BINDIR)/lvmd ./pkg/lvmd && cd -

.PHONY: start-lvmd
start-lvmd: $(TMPDIR)/topolvm $(BINDIR)/lvmd
	mkdir -p $(BACKING_STORE)
	if [ $$(ls -1 $(BACKING_STORE)/backing_store* 2>/dev/null | wc -l) -ne 0 ]; then $(MAKE) stop-lvmd; fi

	for i in $$(seq 3); do \
		mkdir -p $(TMPDIR)/lvmd$$i; \
		truncate --size=5G $(BACKING_STORE)/backing_store$${i}_1; \
		$(SUDO) losetup -f $(BACKING_STORE)/backing_store$${i}_1; \
		$(SUDO) vgcreate -y $(PREFIX)-node$${i}-myvg1 $$($(SUDO) losetup -j $(BACKING_STORE)/backing_store$${i}_1 | cut -d: -f1); \
		truncate --size=5G $(BACKING_STORE)/backing_store$${i}_2; \
		$(SUDO) losetup -f $(BACKING_STORE)/backing_store$${i}_2; \
		$(SUDO) vgcreate -y $(PREFIX)-node$${i}-myvg2 $$($(SUDO) losetup -j $(BACKING_STORE)/backing_store$${i}_2 | cut -d: -f1); \
	done

	for i in $$(seq 3); do \
		$(SUDO) systemd-run \
			--unit=$(PREFIX)-lvmd$$i.service \
			$(BINDIR)/lvmd --config=$(shell pwd)/lvmd$$i.yaml; \
	done

# remove systemd service
# https://superuser.com/questions/513159/how-to-remove-systemd-services
.PHONY: stop-lvmd
stop-lvmd: $(TMPDIR)/topolvm
	$(MAKE) shutdown-kind
	for i in $$(seq 3); do \
		if systemctl is-active -q $(PREFIX)-lvmd$$i.service; then \
			$(SUDO) systemctl stop $(PREFIX)-lvmd$$i.service; \
			$(SUDO) systemctl disable $(PREFIX)-lvmd$$i.service; \
			$(SUDO) rm -f /etc/systemd/system/$(PREFIX)-lvmd$$i.service; \
			$(SUDO) rm -f /usr/lib/systemd/system/$(PREFIX)-lvmd$$i.service; \
		fi; \
		systemctl daemon-reload; \
		systemctl reset-failed; \
		for j in $$(seq 2); do \
			if [ -f $(BACKING_STORE)/backing_store$${i}_$${j} ]; then \
				$(SUDO) vgremove -ffy $(PREFIX)-node$${i}-myvg$${j}; \
				$(SUDO) pvremove -ffy $$($(SUDO) losetup -j $(BACKING_STORE)/backing_store$${i}_$${j} | cut -d: -f1); \
				$(SUDO) losetup -d $$($(SUDO) losetup -j $(BACKING_STORE)/backing_store$${i}_$${j} | cut -d: -f1); \
				rm -f $(BACKING_STORE)/backing_store$${i}_$${j}; \
			fi; \
		done; \
	done

$(TMPDIR)/topolvm:
	git clone https://github.com/topolvm/topolvm.git $@
	cd $@ && git checkout v$(TOPOLVM_VERSION)
	make -C $(TMPDIR)/topolvm/example setup

.PHONY: clean
clean: stop-lvmd
	rm -rf \
		$(BINDIR) \
		$(TMPDIR)
