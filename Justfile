# build the manager binary
build:
    go build -o bin/manager ./cmd/main.go

# run unit tests (excludes e2e)
test:
    go test $(go list ./... | grep -v /e2e) -count=1

# run linter
lint:
    golangci-lint run

# regenerate deepcopy methods and RBAC manifests
generate:
    controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./..."
    controller-gen rbac:roleName=manager-role paths="./..." output:dir=chart/templates

# Linux CI

VCLUSTER_VERSION := "v0.32.0"
K8S_VERSION      := "v1.32.0"
KUBECONFIG_PATH  := env_var('HOME') + "/.kube/vcluster.yaml"

# load kernel modules required for vind (best-effort, passwordless sudo only)
setup-kernel-linux:
    sudo -n modprobe br_netfilter || true
    sudo -n modprobe overlay || true
    sudo -n sysctl -w net.ipv4.ip_forward=1 || true
    sudo -n sysctl -w net.bridge.bridge-nf-call-iptables=1 || true
    sudo -n sysctl -w net.bridge.bridge-nf-call-ip6tables=1 || true

# configure Docker daemon to use cgroupfs (required for vind on GitHub Actions)
setup-docker-linux:
    echo '{"exec-opts":["native.cgroupdriver=cgroupfs"]}' | sudo tee /etc/docker/daemon.json
    sudo systemctl restart docker
    until docker info &>/dev/null; do sleep 1; done
    echo "Docker ready (cgroup driver: $(docker info --format '{{{{.CgroupDriver}}}}'))"

# install vcluster CLI to ~/.local/bin (skips if already installed)
install-vcluster-linux:
    #!/usr/bin/env bash
    set -euo pipefail
    if command -v vcluster &>/dev/null; then
        echo "vcluster already installed: $(vcluster version 2>&1 | head -1)"
    else
        mkdir -p "$HOME/.local/bin"
        curl -fsSL -o "$HOME/.local/bin/vcluster" \
            "https://github.com/loft-sh/vcluster/releases/download/{{VCLUSTER_VERSION}}/vcluster-linux-amd64"
        chmod +x "$HOME/.local/bin/vcluster"
        vcluster version
    fi

# create the host vcluster with docker driver (skips if already exists, resumes if stopped, always connects)
create-host-cluster-linux:
    #!/usr/bin/env bash
    set -euo pipefail
    vcluster use driver docker
    if vcluster list 2>/dev/null | grep -q "host-cluster"; then
        echo "host-cluster already exists, resuming if needed"
        vcluster resume host-cluster 2>/dev/null || true
        just connect-host-cluster-linux
    else
        for attempt in 1 2 3; do
            echo "Creating host-cluster (attempt $attempt/3)..."
            if vcluster create host-cluster --driver docker --connect=false \
                --set controlPlane.distro.k8s.version={{K8S_VERSION}}; then
                echo "host-cluster created successfully"
                break
            fi
            if [ "$attempt" -lt 3 ]; then
                echo "Create failed, cleaning up and retrying in 15s..."
                vcluster delete host-cluster 2>/dev/null || true
                sleep 15
            else
                echo "error: failed to create host-cluster after 3 attempts"
                exit 1
            fi
        done
    fi

# wait for host cluster to reach Running state
wait-host-cluster-linux:
    #!/usr/bin/env bash
    set -euo pipefail
    for i in $(seq 1 24); do
        if vcluster list 2>/dev/null | grep -qi "running"; then
            echo "host-cluster is ready"
            exit 0
        fi
        echo "Waiting for host-cluster... ($i/24)"
        sleep 5
    done
    echo "error: timed out waiting for host-cluster"
    exit 1

# write kubeconfig for the host cluster to ~/.kube/vcluster.yaml
connect-host-cluster-linux:
    mkdir -p "$HOME/.kube"
    vcluster connect host-cluster --print > "{{KUBECONFIG_PATH}}"
    echo "KUBECONFIG={{KUBECONFIG_PATH}}"

# delete the host cluster
delete-host-cluster-linux:
    vcluster use driver docker
    vcluster delete host-cluster

# run the devspace e2e pipeline against the host cluster
e2e-pipeline-linux:
    KUBECONFIG="{{KUBECONFIG_PATH}}" devspace run-pipeline e2e \
        -n vcluster-rancher-operator-system \
        --var=LOCAL_DOCKER_HOST=172.17.0.1 \
        --var=ADMIN_PASSWORD=test123

# full e2e: create host cluster if missing, connect, and run the pipeline
# use `just e2e-linux cleanup=true` to delete the cluster afterwards
e2e-linux cleanup="false":
    #!/usr/bin/env bash
    set -euo pipefail
    export PATH="$HOME/.local/bin:$PATH"
    just setup-kernel-linux
    just setup-docker-linux
    just install-vcluster-linux
    just create-host-cluster-linux
    just wait-host-cluster-linux
    just connect-host-cluster-linux
    KUBECONFIG="{{KUBECONFIG_PATH}}" kubectl get nodes
    just e2e-pipeline-linux
    if [ "{{cleanup}}" = "true" ]; then
        just delete-host-cluster-linux
    fi
