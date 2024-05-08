# scheduler-plugin-demo

## Background

存在这么一种场景：在集群资源紧张的情况下，当一个 pod 被**重新调度**（replica 不变，删 pod）之后，有可能出现 pod 找不到其他合适的节点，此时它原来所属的 node 由于调度来了其他的 pod，也容纳不下它了，于是这个 pod 陷入了 Pending 状态。

我们希望当一个资源**重建**期间，原来的节点为它保存资源，这样，当他找不到其他合适的节点时，至少能回到原来的节点。本项目就是为了达到这个目的，实现了一个 out-of-tree scheduler plugin，通过节点的 annotation 标识保留的资源，当有其他 pod 调度至本节点计算节点资源时，额外考虑为重建的 pod 保留的资源。

## Get Start

本地测试：

> 直接运行，会报端口占用的错误，占用端口的正是 kube-scheduler，因此我们可以暂时删除默认调度器，做法是：
> 把 `/etc/kubernetes/manifests/` 文件夹下的 `kube-scheduler.yaml` 暂时挪到别的地方（比如创建 bak/ 然后把它丢进去）
> 这样做是因为 kube-scheduler 是由 kubelet 负责启动的静态 pod，因此依赖本地 yaml 文件，我们只需要让 kubelet 找不到这个 yaml 文件，pod 自然就么了

```bash
$ sudo su
$ go run cmd/main.go --kube-api-qps=200 --kube-api-burst=300  --leader-elect=false --profiling=false --authentication-kubeconfig=/etc/kubernetes/scheduler.conf --authorization-kubeconfig=/etc/kubernetes/scheduler.conf --kubeconfig=/etc/kubernetes/scheduler.conf  --config=/home/aiedge/github.com/scheduler-plugin-demo/manifests/noderesourcematch/scheduler-config.yaml
```

封装镜像：

```bash
$ make docker
```

推送仓库

```bash
# 仓库地址在 Makefile 中修改
# 修改之后在 ./manifests/all-in-one.yaml 中也要同步修改镜像名（还要手动改，写的有点瓦）
$ make push
```

部署

```bash
$ sudo kubectl apply -f ./manifests/all-in-one.yaml
```

## 测试

节点拓扑：
```bash
# 每个节点的 Allocatable.MilliCPU 为 6000
NAME                  STATUS   ROLES           AGE   VERSION
zsj-dev-master-200    Ready    control-plane   9d    v1.25.3
zsj-dev-worker1-201   Ready    <none>          9d    v1.25.3
```

Example pod：

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: testngx
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: testngx
  template:
    metadata:
      labels:
        app: testngx
    spec:
      schedulerName: test-scheduler
      containers:
        - image: nginx
          imagePullPolicy: IfNotPresent
          name: testngx
          ports:
            - containerPort: 80
          resources:
            requests:
              cpu: "1000m"
              memory: "128Mi"
```

部署之后，在资源充足的情况会被调度到 `zsj-dev-worker1-201`

此时我们给该节点打上 annotation，然后删除 Example pod：

```bash
$ sudo kubectl annotate node zsj-dev-worker1-201 reserve.cpu/a8f6f901-47c9-4475-b1f4-cac44597e173="5000m"
$ sudo kubectl delete po testngx-7f8646dbfd-n5cpf
```

此时可以看到重建的 pod 进入 Pending 状态，进一步 describe，可以发现原因是节点资源不足，证明调度器插件工作正常。

> tips: 要实现 Background 中所说的效果，还需要一个 Controller
