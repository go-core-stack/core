#!/bin/bash

set -ex

sudo docker run -d --network host --name mongodb -e MONGO_INITDB_ROOT_USERNAME=root -e MONGO_INITDB_ROOT_PASSWORD=password mongo:8.0.6
