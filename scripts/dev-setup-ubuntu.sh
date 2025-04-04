#!/bin/bash
set -ex

GOLANG_PACKAGE=${GOLANG_PACKAGE:="go1.24.2.linux-amd64.tar.gz"}

sudo apt update -y

wget https://dl.google.com/go/${GOLANG_PACKAGE}
sudo tar -C /usr/local -xzf ${GOLANG_PACKAGE}
rm -rf ${GOLANG_PACKAGE}
echo "export GOPATH=\$HOME/go" >> ~/.bashrc
echo "export PATH=\$PATH:/usr/local/go/bin:\$HOME/go/bin" >> ~/.bashrc
source ~/.bashrc

# install other dependencies
sudo apt install -y make gcc