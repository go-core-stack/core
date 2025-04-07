ROOT_DIR:=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

.PHONY: all

all: go-format go-vet setup-mongodb-env

setup-mongodb-env:
	${ROOT_DIR}/scripts/mongodb/run-mongodb-dev.sh
	${ROOT_DIR}/scripts/mongodb/run-mongoexpress.sh

cleanup-mongodb-env:
	${ROOT_DIR}/scripts/mongodb/cleanup.sh

go-format:
	go fmt ./...

go-vet:
	go vet ./...
