package main

import (
	"os"

	"github.com/scheduler-plugin-demo/pkg/noderesourcematch"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/cli"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
)

func main() {
	config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	if err != nil {
		inClusterConfig, err := rest.InClusterConfig()
		if err != nil {
			klog.Fatal(err)
		}
		config = inClusterConfig
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatal(err)
	}

	factory := informers.NewSharedInformerFactory(clientset, 0)
	nodeInformer := factory.Core().V1().Nodes()

	controller := noderesourcematch.NewController(clientset, nodeInformer)

	stopCh := make(chan struct{})
	factory.Start(stopCh)
	factory.WaitForCacheSync(stopCh)

	go controller.Run(stopCh)

	
	// Register custom plugins to the scheduler framework.
	command := app.NewSchedulerCommand(
		app.WithPlugin(noderesourcematch.Name, noderesourcematch.New),
	)

	code := cli.Run(command)
	os.Exit(code)
}
