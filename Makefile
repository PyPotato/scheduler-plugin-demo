# 定义 Makefile 变量
IMAGE_REGISTRY    ?= registry.cn-hangzhou.aliyuncs.com/zsj-dev
IMAGE_NAME        ?= test-scheduler
IMAGE_TAG         ?= v0.0.1 # 镜像的版本标签
BINARY_NAME       ?= test-scheduler
PKG               := /home/aiedge/github.com/scheduler-plugin-demo/cmd

.PHONY: build
build: ## 构建 Golang 项目二进制文件
	go build -buildvcs=false -o /home/aiedge/github.com/scheduler-plugin-demo/bin/$(BINARY_NAME) $(PKG)

.PHONY: docker
docker: ## 打包项目为 Docker 镜像
	sudo docker build -t $(IMAGE_REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG) .

.PHONY: push
push: ## 将 Docker 镜像推送到远程镜像仓库
	sudo docker push $(IMAGE_REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

.PHONY: run
run:
	./$(BINARY_NAME)