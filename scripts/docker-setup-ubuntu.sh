# Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
# Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

#!/bin/bash

set -ex

sudo apt install -y docker-buildx
sudo systemctl start docker
sudo systemctl enable docker
sudo apt install -y docker-compose
