# 脚本使用说明

## get-k8s-as-dep.sh

**kubernetes 会对外屏蔽部分不稳定的代码 —— 把部分模块的依赖版本写成 v0.0.0**，然后在 `go.mod` 中用 replace 替换成仓库里的相对路径，这样一来，直接使用 kubernetes 代码是没有问题的，但使用 `go mod` 或 `go get` 获取 kubernetes 仓库作为依赖的时候，就会遇到此类错误：

```bash
k8s.io/apimachinery/pkg/watch: reading k8s.io/apimachinery/go.mod at revision v0.0.0: unknown revision v0.0.0
```

解决方法是：调用此脚本，并指定一个 kubernetes 版本，一方面直接下载官方仓库里被写成 v0.0.0 版本的各个模块的该版本的代码到本地作为依赖，另一方面修改 `go.mod`，将依赖 replace 成指定的版本

```bash
./get-k8s-as-dep.sh v1.25.3
```