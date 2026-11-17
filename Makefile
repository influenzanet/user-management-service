.PHONY: build test install-dev mock docker api api-check user-management-service-app create-user key-generator weekday-assign

PROTO_BUILD_DIR = intermediate

# Where the api repository holding the proto contracts is checked out: by default the api
# submodule, whose gitlink pins the contract version (git submodule update --init). Override
# it to build against another checkout, e.g. make api API_PATH=../api.
API_PATH ?= ./api

include proto-toolchain.mk

DOCKER_OPTS ?= --rm

# Where binary are put
TARGET_DIR ?= ./

# TEST_ARGS = -v | grep -c RUN

VERSION := $(shell git describe --tags --abbrev=0)

TAG ?= $(VERSION)

help:
	@echo "Service building targets"
	@echo "  build : build service command"
	@echo "  test  : run test suites"
	@echo "  docker: build docker image"
	@echo "  install-dev: install dev dependencies"
	@echo "  api: compile protobuf files for go with the pinned toolchain (proto-toolchain.mk)"
	@echo "  api-check: regenerate pkg/api and fail if it differs from the committed code"
	@echo "Env:"
	@echo "  DOCKER_OPTS : default docker build options (default : $(DOCKER_OPTS))"
	@echo "  TEST_ARGS : Arguments to pass to go test call"

# pkg/api is deleted and replaced only once every proto has been generated: a protoc that
# fails, or cannot run on this machine, stops the recipe first and leaves pkg/api alone.
api: $(PROTO_TOOLS)
	if [ ! -d $(PROTO_BUILD_DIR) ]; then mkdir -p $(PROTO_BUILD_DIR); else  find $(PROTO_BUILD_DIR) -type f -delete &&  mkdir -p $(PROTO_BUILD_DIR); fi
	for f in $(API_PATH)/user_management/*.proto; do $(PROTOC_CMD) $$f --proto_path=$(API_PATH) --go_out=$(PROTO_BUILD_DIR) --go-grpc_out=$(PROTO_BUILD_DIR) || exit 1; done
	find "./pkg/api" -delete
	mv $(PROTO_BUILD_DIR)/github.com/influenzanet/user-management-service/pkg/api pkg/api
	find $(PROTO_BUILD_DIR) -delete

# Drift check: fails when pkg/api is not what the pinned toolchain generates from the pinned
# contracts (a hand edit, a proto change not regenerated, another toolchain, a stale file).
api-check: api
	git diff --exit-code -- pkg/api
	@untracked="$$(git ls-files --others --exclude-standard -- pkg/api)"; \
	test -z "$$untracked" || { echo "generated but not committed:"; echo "$$untracked"; exit 1; }

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
	mockgen -source=../messaging-service/pkg/api/messaging_service/message-service_grpc.pb.go MessagingServiceApiClient > test/mocks/messaging_service/messaging_service.go
	mockgen github.com/influenzanet/logging-service/pkg/api LoggingServiceApiClient > test/mocks/logging_service/logging_service.go

docker:
	docker build -t  github.com/influenzanet/user-management-service:$(TAG)  -f build/docker/Dockerfile $(DOCKER_OPTS) .
