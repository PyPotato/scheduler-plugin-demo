package noderesourcematch

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

// 插件名
const (
	Name              = "noderesourcematch-plugin"
	preFilterStateKey = "PreFilter" + Name
	annotationKeyName = "reserve.kubernetes.io/resources"
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
var _ = framework.PreFilterPlugin(&NodeResourceMatch{})

func (nrm *NodeResourceMatch) Name() string {
	return Name
}

func (nrm *NodeResourceMatch) PreFilterExtensions() framework.PreFilterExtensions {
	return nil
}

func (nrm *NodeResourceMatch) PreFilter(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod) (*framework.PreFilterResult, *framework.Status) {
	klog.Info("Writing SycleState of ", preFilterStateKey)
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
	klog.Info("Into Filter ", nodeInfo.Node().GetName())
	s, err := getPreFilterState(cycleState)
	if err != nil {
		return framework.AsStatus(err)
	}

	var insufficientResources []InsufficientResource
	//* 为其保留资源的 pod 可以被调度过来
	if nodeName, exists := NodeResourceCache.ReservedList[string(pod.ObjectMeta.OwnerReferences[0].UID)]; exists && nodeName == nodeInfo.Node().GetName() {
		klog.Info(nodeName, " has resources reserved for pod ", pod.Name)
		insufficientResources = make([]InsufficientResource, 0)
	} else {
		klog.Info("No reserved resources in node ", nodeName, " for pod ", pod.Name)
		insufficientResources = fitsRequest(s, nodeInfo)
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

func fitsRequest(podRequest *preFilterState, nodeInfo *framework.NodeInfo) []InsufficientResource {
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
		klog.Info("Request None")
		return insufficientResources
	}

	Reserved := GetReservedResourcesFromCache(nodeInfo)
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
	klog.Info("Total(Allocatable) MilliCPU: ", nodeInfo.Allocatable.MilliCPU)
	klog.Info("podRequest MilliCPU: ", podRequest.MilliCPU)
	klog.Info("Reserved MilliCPU: ", Reserved.MilliCPU)
	klog.Info("Available MilliCPU: ", nodeInfo.Allocatable.MilliCPU-nodeInfo.Requested.MilliCPU-Reserved.MilliCPU)
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
	klog.Info("Total(Allocatable) Memory: ", nodeInfo.Allocatable.Memory)
	klog.Info("podRequest Memory: ", podRequest.Memory)
	klog.Info("Reserved Memory: ", Reserved.Memory)
	klog.Info("Available Memory: ", nodeInfo.Allocatable.Memory-nodeInfo.Requested.Memory-Reserved.Memory)
	if podRequest.EphemeralStorage > (nodeInfo.Allocatable.EphemeralStorage - nodeInfo.Requested.EphemeralStorage - Reserved.EphemeralStorage) {
		insufficientResources = append(insufficientResources, InsufficientResource{
			ResourceName: v1.ResourceEphemeralStorage,
			Reason:       "Insufficient ephemeral-storage",
			Requested:    podRequest.EphemeralStorage,
			Used:         nodeInfo.Requested.EphemeralStorage + Reserved.EphemeralStorage,
			Capacity:     nodeInfo.Allocatable.EphemeralStorage,
		})
	}

	return insufficientResources
}

// func GetReservedResources(nodeInfo *framework.NodeInfo) *framework.Resource {
// 	// Get reserved resources from node annotations
// 	klog.Info("Collecting reserved resources")
// 	reservedResources := &framework.Resource{}

// 	node := nodeInfo.Node()
// 	// Select all annotations whose key is "reserve.{resource-type}/{owner-uid}"
// 	for k, v := range node.Annotations {
// 		if !IsReserveAnnotation(k) {
// 			continue
// 		}
// 		// Parse the annotation value
// 		rName, rQuant, _, err := parseReserveAnnotation(k, v)
// 		if err != nil {
// 			return reservedResources
// 		}
// 		// 这里假设value符合各种资源类型用量的单位标准
// 		switch rName {
// 		case v1.ResourceCPU:
// 			klog.Info("Reserving cpu: ", rQuant.MilliValue())
// 			reservedResources.MilliCPU += rQuant.MilliValue()
// 		case v1.ResourceMemory:
// 			klog.Info("Reserving memory: ", rQuant.Value())
// 			reservedResources.Memory += rQuant.Value()
// 		case v1.ResourceEphemeralStorage:
// 			klog.Info("Reserving ephemeralStorage: ", rQuant.Value())
// 			reservedResources.EphemeralStorage += rQuant.Value()
// 		}
// 	}
// 	return reservedResources
// }

func GetReservedResourcesFromCache(nodeInfo *framework.NodeInfo) framework.Resource {
	nodeName := nodeInfo.Node().GetName()
	reserved := NodeResourceCache.ReservedResources[nodeName]
	return reserved
}

// func IsReserveAnnotation(key string) bool {
// 	return key[:8] == "reserve."
// }

// func parseReserveAnnotation(key, value string) (v1.ResourceName, resource.Quantity, string, error) {
// 	var rName v1.ResourceName
// 	rQuant, err := resource.ParseQuantity(value)
// 	if err != nil {
// 		return rName, rQuant, "", err
// 	}

// 	parts := strings.Split(key, "/")
// 	resourceType := parts[0]
// 	rOwnerUid := parts[1]
// 	switch resourceType {
// 	case "reserve.cpu":
// 		rName = v1.ResourceCPU
// 	case "reserve.memory":
// 		rName = v1.ResourceMemory
// 	case "reserve.ephemeral-storage":
// 		rName = v1.ResourceEphemeralStorage
// 	}

// 	return rName, rQuant, rOwnerUid, nil
// }

func New(configuration runtime.Object, h framework.Handle) (framework.Plugin, error) {

	return &NodeResourceMatch{handle: h}, nil
}
