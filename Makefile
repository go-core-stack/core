# Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
# Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

ROOT_DIR:=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

.PHONY: all

all: go-format go-vet go-lint setup-mongodb-env

test: go-format go-vet go-lint
	go test -v ./...
setup-mongodb-env:
	${ROOT_DIR}/scripts/mongodb/run-mongodb-dev.sh
	${ROOT_DIR}/scripts/mongodb/run-mongoexpress.sh

cleanup-mongodb-env:
	${ROOT_DIR}/scripts/mongodb/cleanup.sh

go-format:
	go fmt ./...

go-vet:
	go vet ./...

go-lint:
	golangci-lint run
