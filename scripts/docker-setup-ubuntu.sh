#!/bin/bash

set -ex

sudo apt install -y docker-buildx
sudo systemctl start docker
sudo systemctl enable docker
sudo apt install -y docker-compose
