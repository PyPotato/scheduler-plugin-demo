package noderesourcematch

import (
	"context"
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

// 插件名
const (
	Name = "noderesourcematch-plugin"

	preFilterStateKey = "PreFilter" + Name
)

// 定义 plugin struct
type NodeResourceMatch struct {
	handle framework.Handle
}

// node resources
type preFilterState struct {
	framework.Resource
}

// InsufficientResource describes what kind of resource limit is hit and caused the pod to not fit the node.
type InsufficientResource struct {
	ResourceName v1.ResourceName
	// We explicitly have a parameter for reason to avoid formatting a message on the fly
	// for common resources, which is expensive for cluster autoscaler simulations.
	Reason    string
	Requested int64
	Used      int64
	Capacity  int64
}

var _ = framework.FilterPlugin(&NodeResourceMatch{})

func (nrm *NodeResourceMatch) Name() string {
	return Name
}

func (nrm *NodeResourceMatch) PreFilter(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod) (*framework.PreFilterResult, *framework.Status) {
	cycleState.Write(preFilterStateKey, computePodResourceRequest(pod))
	return nil, nil
}

func computePodResourceRequest(pod *v1.Pod) *preFilterState {
	result := &preFilterState{}
	for _, container := range pod.Spec.Containers {
		result.Add(container.Resources.Requests)
	}
	// take max_resource(sum_pod, any_init_container)
	for _, container := range pod.Spec.InitContainers {
		result.SetMaxResource(container.Resources.Requests)
	}

	// If Overhead is being utilized, add to the total requests for the pod
	if pod.Spec.Overhead != nil {
		result.Add(pod.Spec.Overhead)
	}
	return result
}

// 按道理，执行到自定义Filter插件的时候，in-tree的PreFilter插件已经执行完了，所以节点的资源状态(NodeInfo)认为已经有了
func (nrm *NodeResourceMatch) Filter(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	s, err := getPreFilterState(cycleState)
	if err != nil {
		return framework.AsStatus(err)
	}

	insufficientResources, fitserr := fitsRequest(s, nodeInfo)
	if fitserr != nil {
		//! 这里不确定会对后续调度行为产生什么具体的后果
		return framework.NewStatus(framework.Error, "error when get insufficientResources")
	}

	if len(insufficientResources) != 0 {
		// We will keep all failure reasons.
		failureReasons := make([]string, 0, len(insufficientResources))
		for i := range insufficientResources {
			failureReasons = append(failureReasons, insufficientResources[i].Reason)
		}
		return framework.NewStatus(framework.Unschedulable, failureReasons...)
	}

	return framework.NewStatus(framework.Success, "schedule success")
}

// Clone the prefilter state.
func (s *preFilterState) Clone() framework.StateData {
	return s
}

func getPreFilterState(cycleState *framework.CycleState) (*preFilterState, error) {
	c, err := cycleState.Read(preFilterStateKey)
	if err != nil {
		// preFilterState doesn't exist, likely PreFilter wasn't invoked.
		return nil, fmt.Errorf("error reading %q from cycleState: %w", preFilterStateKey, err)
	}

	s, ok := c.(*preFilterState)
	if !ok {
		return nil, fmt.Errorf("%+v  convert to NodeResourcesFit.preFilterState error", c)
	}
	return s, nil
}

func fitsRequest(podRequest *preFilterState, nodeInfo *framework.NodeInfo) ([]InsufficientResource, error) {
	insufficientResources := make([]InsufficientResource, 0, 4)

	// 检查最大Pod限制
	allowedPodNumber := nodeInfo.Allocatable.AllowedPodNumber
	if len(nodeInfo.Pods)+1 > allowedPodNumber {
		insufficientResources = append(insufficientResources, InsufficientResource{
			ResourceName: v1.ResourcePods,
			Reason:       "Too many pods",
			Requested:    1,
			Used:         int64(len(nodeInfo.Pods)),
			Capacity:     int64(allowedPodNumber),
		})
	}

	if podRequest.MilliCPU == 0 && podRequest.Memory == 0 &&
		podRequest.EphemeralStorage == 0 && len(podRequest.ScalarResources) == 0 {
		return insufficientResources, nil
	}

	Reserved, err := GetReservedResources(nodeInfo)
	if err != nil {
		return insufficientResources, nil
	}
	// 检查CPU余量，考虑预留资源
	if podRequest.MilliCPU > (nodeInfo.Allocatable.MilliCPU - nodeInfo.Requested.MilliCPU - Reserved.MilliCPU) {
		insufficientResources = append(insufficientResources, InsufficientResource{
			ResourceName: v1.ResourceCPU,
			Reason:       "Insufficient cpu",
			Requested:    podRequest.MilliCPU,
			Used:         nodeInfo.Requested.MilliCPU + Reserved.MilliCPU,
			Capacity:     nodeInfo.Allocatable.MilliCPU,
		})
	}
	// 检查内存
	if podRequest.Memory > (nodeInfo.Allocatable.Memory - nodeInfo.Requested.Memory - Reserved.Memory) {
		insufficientResources = append(insufficientResources, InsufficientResource{
			ResourceName: v1.ResourceMemory,
			Reason:       "Insufficient memory",
			Requested:    podRequest.Memory,
			Used:         nodeInfo.Requested.Memory + Reserved.Memory,
			Capacity:     nodeInfo.Allocatable.Memory,
		})
	}
	if podRequest.EphemeralStorage > (nodeInfo.Allocatable.EphemeralStorage - nodeInfo.Requested.EphemeralStorage - Reserved.EphemeralStorage) {
		insufficientResources = append(insufficientResources, InsufficientResource{
			ResourceName: v1.ResourceEphemeralStorage,
			Reason:       "Insufficient ephemeral-storage",
			Requested:    podRequest.EphemeralStorage,
			Used:         nodeInfo.Requested.EphemeralStorage + Reserved.EphemeralStorage,
			Capacity:     nodeInfo.Allocatable.EphemeralStorage,
		})
	}

	return insufficientResources, nil
}

func GetReservedResources(nodeInfo *framework.NodeInfo) (*framework.Resource, error) {
	// Get reserved resources from node annotations
	reservedResources := &framework.Resource{}
	node := nodeInfo.Node()
	// Select all annotations whose key is "reserve.{resource-type}/{owner-uid}"
	for k, v := range node.Annotations {
		if !IsReserveAnnotation(k) {
			continue
		}
		// Parse the annotation value
		rName, rQuant, _, err := parseReserveAnnotation(k, v)
		if err != nil {
			return reservedResources, err
		}
		// 这里假设value符合各种资源类型用量的单位标准
		switch rName {
		case v1.ResourceCPU:
			reservedResources.MilliCPU += rQuant.MilliValue()
		case v1.ResourceMemory:
			reservedResources.Memory += rQuant.Value()
		case v1.ResourceEphemeralStorage:
			reservedResources.EphemeralStorage += rQuant.Value()
		}
	}
	return reservedResources, nil
}

func IsReserveAnnotation(key string) bool {
	return key[:8] == "reserve."
}

func parseReserveAnnotation(key, value string) (v1.ResourceName, resource.Quantity, string, error) {
	var rName v1.ResourceName
	rQuant, err := resource.ParseQuantity(value)
	if err != nil {
		return rName, rQuant, "", err
	}

	parts := strings.Split(key, "/")
	resourceType := parts[0]
	rOwnerUid := parts[1]
	switch resourceType {
	case "reserve.cpu":
		rName = v1.ResourceCPU
	case "reserve.memory":
		rName = v1.ResourceMemory
	case "reserve.ephemeral-storage":
		rName = v1.ResourceEphemeralStorage
	}

	return rName, rQuant, rOwnerUid, nil
}

func New(configuration runtime.Object, h framework.Handle) (framework.Plugin, error) {

	return &NodeResourceMatch{handle: h}, nil
}
