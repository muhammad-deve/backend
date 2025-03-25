CURRENT_DIR := $(shell pwd)
APP_DIR := $(CURRENT_DIR)/app
CMD_DIR := $(APP_DIR)/cmd
PB_DATA_DIR := $(APP_DIR)/artifacts/pb_data

REGISTRY=docker.io/oybekzdockerid
PROJECT_NAME=pocketbase-frst
ENV_TAG=dev
IMAGE_NAME=${PROJECT_NAME}-${ENV_TAG}
TAG=latest

build-image-dev:
	docker build --rm -t ${REGISTRY}/${IMAGE_NAME}:${TAG} .

push-image-dev:
	docker push ${REGISTRY}/${IMAGE_NAME}:${TAG}

run-app:
	cd ${CMD_DIR} && go run main.go serve --dir=${PB_DATA_DIR}

run-app-watch:
	cd $(APP_DIR) && nodemon --watch './**/*.go' --ignore 'app/artifacts/migrations/**' --signal SIGTERM --exec go run cmd/main.go serve --dir=${PB_DATA_DIR}