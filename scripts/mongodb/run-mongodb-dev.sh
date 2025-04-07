# Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
# Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

#!/bin/bash

set -ex

SCRIPT_DIR=$(dirname $0)

if test -e "$SCRIPT_DIR/mongod.conf"
then
    CONFIG_FLAG="--config /etc/mymongo/mongod.conf"
    VOLUME_FLAG="-v $SCRIPT_DIR:/etc/mymongo"

    if test -e "$SCRIPT_DIR/mongod_key"
    then
        echo "skipping key generation"
    else
        openssl rand -base64 756 > $SCRIPT_DIR/mongod_key
        chmod 400 $SCRIPT_DIR/mongod_key
    fi
    sudo chown 999:999 $SCRIPT_DIR/mongod_key
fi

sudo docker run -d --network host --name mongodb ${VOLUME_FLAG} -e MONGO_INITDB_ROOT_USERNAME=root -e MONGO_INITDB_ROOT_PASSWORD=password mongo:8.0.6 ${CONFIG_FLAG}
sleep 10
sudo docker exec -i mongodb mongosh -u root -p password --eval 'rs.initiate()'
