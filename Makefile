.PHONY: build test install-dev mock docker api user-management-service-app create-user key-generator weekday-assign

PROTO_BUILD_DIR = intermediate
DOCKER_OPTS ?= --rm

# Where binary are put
TARGET_DIR ?= ./

# TEST_ARGS = -v | grep -c RUN

VERSION := $(shell git describe --tags --abbrev=0)

help:
	@echo "Service building targets"
	@echo "  build : build service command"
	@echo "  test  : run test suites"
	@echo "  docker: build docker image"
	@echo "  install-dev: install dev dependencies"
	@echo "  api: compile protobuf files for go"
	@echo "Env:"
	@echo "  DOCKER_OPTS : default docker build options (default : $(DOCKER_OPTS))"
	@echo "  TEST_ARGS : Arguments to pass to go test call"

api:
	if [ ! -d $(PROTO_BUILD_DIR) ]; then mkdir -p $(PROTO_BUILD_DIR); else  find $(PROTO_BUILD_DIR) -type f -delete &&  mkdir -p $(PROTO_BUILD_DIR); fi
	find ./api/user_management/*.proto -maxdepth 1 -type f -exec protoc {} --proto_path=./api --go_out=$(PROTO_BUILD_DIR) --go_grpc_out=$(PROTO_BUILD_DIR) \;
	find "./pkg/api" -delete
	mv $(PROTO_BUILD_DIR)/github.com/influenzanet/user-management-service/pkg/api pkg/api
	find $(PROTO_BUILD_DIR) -delete

key-generator:
	go build -o $(TARGET_DIR) ./tools/key-generator

create-admin-user:
	go build -o $(TARGET_DIR) ./tools/create-admin-user

user-management-service-app:
	go build -o $(TARGET_DIR) ./cmd/user-management-service-app

weekday-assign:
	go build -o $(TARGET_DIR) ./tools/weekday-assign

build: user-management-service-app key-generator create-admin-user weekday-assign

test:
	./test/test.sh $(TEST_ARGS)

install-dev:
	go get github.com/golang/mock/gomock
	go install github.com/golang/mock/mockgen

mock:
	# messaging service repo has to be in the relative path as here:
	mockgen -source=../messaging-service/pkg/api/messaging_service/message-service.pb.go MessagingServiceApiClient > test/mocks/messaging_service/messaging_service.go
	mockgen github.com/influenzanet/logging-service/pkg/api LoggingServiceApiClient > test/mocks/logging_service/logging_service.go

docker:
	docker build -t  github.com/influenzanet/user-management-service:$(VERSION)  -f build/docker/Dockerfile $(DOCKER_OPTS) .
