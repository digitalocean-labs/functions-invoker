#!/bin/bash

###
# This script lists all images containing the string '/action' (the runtime images)
# and docker removes these images if they are not referenced by the current runtimes
# manifest. It also cleans up old versions of the 'docker-firewall' image.
###

# exit if we cannot process runtime manifest or list of current images
set -e

RUNTIMES_MANIFEST=${1?Runtime manifest required as an argument.}
RUNTIME_IMAGES=$(echo "$RUNTIMES_MANIFEST" | jq -r '.runtimes | . as $r | keys[] | $r[.] | map(.image.prefix+"/"+.image.name+":"+.image.tag) | .[]')
BLACKBOX_IMAGES=$(echo "$RUNTIMES_MANIFEST" | jq -r 'select(.blackboxes != null) | .blackboxes | map(.prefix+"/"+.name+":"+.tag) | .[]')
EXISTING_RUNTIME_IMAGES=$(docker images --filter=dangling=false --format "{{.Repository}}:{{.Tag}}" | grep /action || :)
EXISTING_FIREWALL_IMAGES=$(docker images --filter=dangling=false --format "{{.Repository}}:{{.Tag}}" | grep docker-firewall || :)

echo "---------------------- Purging old action runtimes"

# do not exit if docker rmi fails
set +e

for image in $EXISTING_RUNTIME_IMAGES
do
    if [[ ! "${RUNTIME_IMAGES[*]} ${BLACKBOX_IMAGES[*]}" == *"${image}"* ]]; then
        echo "docker rmi -f $image"
        docker rmi -f "$image"
    else
        echo "saving $image"
    fi
done

# cleanup firewall images
if [[ -n "$FIREWALL_IMAGE_TAG" ]]; then
    for image in $EXISTING_FIREWALL_IMAGES
    do
        if [[ ! "$image" == *"$FIREWALL_IMAGE_TAG"* ]]; then
            echo "docker rmi -f $image"
            docker rmi -f "$image"
        else
            echo "saving $image"
        fi
    done
fi

# lastly cleanup dangling images
docker image prune -f

# Always exit successfully. We don't want to fail due to conflict errors and thelike.
exit 0
