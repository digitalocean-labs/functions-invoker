#!/bin/bash

docker -v

echo "docker daemon configuration"
cat /host/etc/docker/daemon.json

# Make sure userns-remap is part of the config. This process is idempotent, so run it without a
# check beforehand.
mv /host/etc/docker/daemon.json /host/etc/docker/daemon.json.bak
jq '. + {"userns-remap": "default"}' /host/etc/docker/daemon.json.bak > /host/etc/docker/daemon.json

# The StatefulSet's ordinal becomes the invoker's index.
export ID=${HOSTNAME##*-}

# Install gVisor as a docker runtime if not already done or if we want a different version.
if ! docker info -f "{{.Runtimes}}" | grep runsc || [[ $(runsc -version | head -n1) != $(chroot /host runsc -version | head -n1) && $ID -lt $GVISOR_UPGRADE_COUNT ]]
then
    echo "Installing gVisor version $GVISOR_VERSION"
    cp /usr/bin/runsc /usr/bin/containerd-shim-runsc-v1 /host/usr/local/bin
    chroot /host runsc install
else
    echo "gVisor already installed. Skipping installation."
fi

echo "Restarting docker daemon to enfore consistent state"
# Avoid hitting the 'start-limit-hit'.
chroot /host systemctl reset-failed docker.service
chroot /host systemctl restart docker

if ! docker info
then
    echo "docker daemon not running properly. Restarting the pod as the socket might've been recreated."
    exit 1
fi

# Exit if any of the setup work fails.
set -e

echo "Remove all running containers"
docker ps -aq --filter status=paused | xargs -r docker unpause
docker ps -aq | xargs -r docker rm -f

echo "Prepulling all runtime images"
./prepull.sh "$RUNTIMES_MANIFEST"

echo "Purging outdated images"
./image-purge.sh "$RUNTIMES_MANIFEST"

echo "Launching firewall container"
FIREWALL_IMAGE="$DOCKER_REGISTRY/docker-firewall:$FIREWALL_IMAGE_TAG"
docker pull "$FIREWALL_IMAGE"
docker tag "$FIREWALL_IMAGE" docker-firewall
docker run -d --name firewall --net=host --userns=host --cap-add=NET_ADMIN --restart always --entrypoint sleep docker-firewall infinity

echo "Flush per-container firewalls"
# This can fail if its run against a fresh node, so we ignore the failure.
docker exec firewall iptables -F PER-CONTAINER-FIREWALL || true

echo "Creating network for containers"
docker network rm functions || true
export CONTAINERS_NETWORK="functions"
export CONTAINERS_SUBNET="172.18.0.0/16"
export CONTAINERS_BRIDGE_NAME="functions0"
# Most of these settings have been copied over from the default bridge network.
# Note that icc (inter-container-connectivity) is disabled as we don't want the
# functions containers to talk to each other.
docker network create --driver bridge --subnet "$CONTAINERS_SUBNET" --gateway "172.18.0.1" \
    --opt "com.docker.network.bridge.name"="$CONTAINERS_BRIDGE_NAME" \
    --opt "com.docker.network.bridge.enable_icc"="false" \
    --opt "com.docker.network.bridge.enable_ip_masquerade"="true" \
    --opt "com.docker.network.bridge.host_binding_ipv4"="0.0.0.0" \
    --opt "com.docker.network.driver.mtu"="1500" \
    $CONTAINERS_NETWORK

echo "nameserver $CONTAINERS_DNS_SERVERS" > /host/etc/docker/functions.resolv.conf

exec /invoker
