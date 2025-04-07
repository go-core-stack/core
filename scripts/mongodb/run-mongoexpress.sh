#!/bin/bash

set -ex

sudo docker run -d --network host --name mongo-express -e ME_CONFIG_MONGODB_ADMINUSERNAME=root -e ME_CONFIG_MONGODB_ADMINPASSWORD=password -e ME_CONFIG_MONGODB_SERVER=localhost mongo-express:1.0.2-20-alpine3.19
