package noderesourcematch

import (
	"encoding/json"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	informer "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	coreLister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

// 监听 node，当 node 新增、更新 Annotations["reserve.kubernetes.io/resources"] 时，同步到 cache

var NodeResourceCache = struct {
	ReservedResources map[string]framework.Resource
	ReservedList      map[string]string
}{
	ReservedResources: make(map[string]framework.Resource),
	ReservedList:      make(map[string]string),
}

const (
	workNum  = 1
	maxRetry = 3
)

type controller struct {
	client     kubernetes.Interface
	nodeLister coreLister.NodeLister
	queue      workqueue.RateLimitingInterface
}

type ReservationItem struct {
	OwnerType         string             `json:"owner_type"`
	OwnerUID          string             `json:"owner_uid"`
	PodName           string             `json:"pod_name"`
	ReservedResources []ReservedResource `json:"reserved_resources"`
}

type ReservedResource struct {
	ResourceType     v1.ResourceName   `json:"resource_type"`
	ReservedQuantity resource.Quantity `json:"reserved_quanty"`
}

func (c *controller) syncAnnotation(key string) error {
	klog.Info("Syncing Annotation")
	_, nodeName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	node, err := c.nodeLister.Get(nodeName)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	annotationStr, ok := node.Annotations[annotationKeyName]
	// Parse newVal into the resource format
	if ok {
		formattedVal, err := parseAnnotation(annotationStr)
		if err != nil {
			klog.Error("Failed to format: ", err)
			return err
		}
		resources := transform2Resource(formattedVal, nodeName)

		NodeResourceCache.ReservedResources[node.Name] = resources
	} else {
		delete(NodeResourceCache.ReservedResources, node.Name)
	}

	return nil
}

func parseAnnotation(newValStr string) ([]ReservationItem, error) {
	var reservedResources []ReservationItem
	err := json.Unmarshal([]byte(newValStr), &reservedResources)
	if err != nil {
		klog.Error("Failed to unmarshal annotation value: ", err)
		return nil, err
	}
	return reservedResources, nil
}

func transform2Resource(reservedResources []ReservationItem, nodeName string) framework.Resource {
	// Maintain NodeResourceCache.ReservedList in the mean while
	newReservedList := make(map[string]string)
	var resource framework.Resource
	for _, item := range reservedResources {
		ownerUid := item.OwnerUID
		newReservedList[ownerUid] = nodeName
		for _, reservedResources := range item.ReservedResources {
			switch reservedResources.ResourceType {
			case v1.ResourceCPU:
				resource.MilliCPU += reservedResources.ReservedQuantity.Value()
			case v1.ResourceMemory:
				resource.Memory += reservedResources.ReservedQuantity.Value()
			}
		}

	}
	// Update NodeResourceCache.ReservedList
	NodeResourceCache.ReservedList = newReservedList
	return resource
}

func (c *controller) onNodeUpdated(oldObj interface{}, newObj interface{}) {
	newNode := newObj.(*v1.Node)
	oldNode := oldObj.(*v1.Node)

	if newVal, newExists := newNode.Annotations[annotationKeyName]; newExists {
		oldVal, oldExists := oldNode.Annotations[annotationKeyName]

		if oldExists && newVal == oldVal {
			return
		}
	}
	c.enqueue(newObj)
}

func (c *controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
	}
	c.queue.Add(key)
}

func (c *controller) Run(stopCh <-chan struct{}) {
	klog.Info("Running")
	for i := 0; i < workNum; i++ {
		go wait.Until(c.worker, time.Minute, stopCh)
	}
	<-stopCh
}

func (c *controller) worker() {
	for c.processNextItem() {
	}
}

func (c *controller) processNextItem() bool {
	item, shutshown := c.queue.Get()
	if shutshown {
		return false
	}
	defer c.queue.Done(item)

	key := item.(string)

	// 调谐主逻辑
	err := c.syncAnnotation(key)
	if err != nil {
		c.handleErr(key, err)
	}
	return true
}

func (c *controller) handleErr(key string, err error) {
	// Enqueue & retry
	if c.queue.NumRequeues(key) <= maxRetry {
		c.queue.AddRateLimited(key)
		return
	}

	runtime.HandleError(err)
	c.queue.Forget(key)
}

func NewController(client kubernetes.Interface, nodeInformer informer.NodeInformer) controller {
	c := controller{
		client:     client,
		nodeLister: nodeInformer.Lister(),
		queue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "resourcereserve"),
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: c.onNodeUpdated,
	})

	return c
}
