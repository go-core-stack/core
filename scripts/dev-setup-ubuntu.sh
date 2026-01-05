# Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
# Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

#!/bin/bash
set -ex

GOLANG_PACKAGE=${GOLANG_PACKAGE:="go1.24.2.linux-amd64.tar.gz"}

sudo apt update -y
# install other dependencies
sudo apt install -y make gcc

# install golang package
wget https://dl.google.com/go/${GOLANG_PACKAGE}
sudo tar -C /usr/local -xzf ${GOLANG_PACKAGE}
rm -rf ${GOLANG_PACKAGE}
echo "export GOPATH=\$HOME/go" >> ~/.bashrc
echo "export PATH=\$PATH:/usr/local/go/bin:\$HOME/go/bin" >> ~/.bashrc
source ~/.bashrc

# install golang lint cli
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.0.2

# install protobuf compiler
sudo apt install -y protobuf-compiler

# install protoc-gen-go
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6

# install protoc-gen-go-grpc
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1

# install protoc-gen-grpc-gateway
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.26.3