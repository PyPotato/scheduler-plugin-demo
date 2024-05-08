# 使用官方 Golang 镜像作为构建环境
FROM golang:1.21-alpine as builder

# 设定工作目录
WORKDIR /app

# 添加 go mod 文件并下载依赖
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download

# 添加源码文件
COPY . .

# 编译项目，生成二进制文件
RUN CGO_ENABLED=0 GOOS=linux go build -o ./bin/test-scheduler ./cmd/main.go

# 使用 alpine 镜像作为基础镜像
FROM alpine:3.14

# 将上一阶段生成的二进制文件拷贝到当前镜像
COPY --from=builder /app/bin/test-scheduler /app/test-scheduler

# 启动时运行二进制文件
ENTRYPOINT ["/app/test-scheduler"]