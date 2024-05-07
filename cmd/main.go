package main

import (
	"os"

	"k8s.io/component-base/cli"
	"github.com/scheduler-plugin-demo/pkg/noderesourcematch"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
)

func main() {
	// Register custom plugins to the scheduler framework.
	command := app.NewSchedulerCommand(
		app.WithPlugin(noderesourcematch.Name, noderesourcematch.New),
	)
	
	code := cli.Run(command)
	os.Exit(code)
}