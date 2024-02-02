#!/bin/bash

###
# This script pulls images specified in the runtime manifest given on the command line.
###

set -e

THE_MANIFEST=${1?Runtime manifest required as an argument.}
BASE_IMAGES=$(echo $THE_MANIFEST | jq -r '.runtimes | . as $r | keys[] | $r[.] | map(.image.prefix+"/"+.image.name+":"+.image.tag) | .[]')
OTHER_IMAGES=$(echo $THE_MANIFEST | jq -r 'select(.blackboxes != null) | .blackboxes | map(.prefix+"/"+.name+":"+.tag) | .[]')

echo "---------------------- Pulling images"
for image in $BASE_IMAGES $OTHER_IMAGES
do
    echo "docker pull $image"
    docker pull $image
done
