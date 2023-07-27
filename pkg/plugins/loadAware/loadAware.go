/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package loadAware

import (
	"context"
	"fmt"
	"sort"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"sigs.k8s.io/descheduler/pkg/api"
	"sigs.k8s.io/descheduler/pkg/descheduler"
	"sigs.k8s.io/descheduler/pkg/descheduler/evictions"
	nodeutil "sigs.k8s.io/descheduler/pkg/descheduler/node"
	podutil "sigs.k8s.io/descheduler/pkg/descheduler/pod"
	"sigs.k8s.io/descheduler/pkg/framework/pluginregistry"
	"sigs.k8s.io/descheduler/pkg/framework/types"
	frameworktypes "sigs.k8s.io/descheduler/pkg/framework/types"
	"sigs.k8s.io/descheduler/pkg/utils"
)

func init() {
	descheduler.SetupPlugins()
	pluginregistry.Register(LoadAwarePluginName, NewLoadAware, &LoadAware{}, &LoadAwareArgs{}, ValidateLoadAwareArgs, SetDefaults_LoadAwareArgs, pluginregistry.PluginRegistry)
}

const LoadAwarePluginName = "LoadAware"

type LoadAware struct {
	handle    types.Handle
	args      *LoadAwareArgs
	podFilter func(pod *v1.Pod) bool
}

var _ types.BalancePlugin = &LoadAware{}

// NewLoadAware builds plugin from its arguments while passing a handle
func NewLoadAware(args runtime.Object, handle types.Handle) (types.Plugin, error) {
	loadAwareArgs, ok := args.(*LoadAwareArgs)
	if !ok {
		return nil, fmt.Errorf("want args to be of type HighNodeUtilizationArgs, got %T", args)
	}
	podFilter, err := podutil.NewOptions().
		WithFilter(handle.Evictor().Filter).
		BuildFilterFunc()
	if err != nil {
		return nil, fmt.Errorf("error initializing pod filter function: %v", err)
	}

	return &LoadAware{
		handle:    handle,
		args:      loadAwareArgs,
		podFilter: podFilter,
	}, nil
}

// Name retrieves the plugin name
func (h *LoadAware) Name() string {
	return LoadAwarePluginName
}

// Balance extension point implementation for the plugin
func (h *LoadAware) Balance(ctx context.Context, nodes []*v1.Node) *types.Status {

	useDeviationThresholds := h.args.UseDeviationThresholds
	thresholds := h.args.Thresholds
	targetThresholds := h.args.TargetThresholds

	// check if Pods/CPU/Mem are set, if not, set them to 100
	if _, ok := thresholds[v1.ResourcePods]; !ok {
		if useDeviationThresholds {
			thresholds[v1.ResourcePods] = MinResourcePercentage
			targetThresholds[v1.ResourcePods] = MinResourcePercentage
		} else {
			thresholds[v1.ResourcePods] = MaxResourcePercentage
			targetThresholds[v1.ResourcePods] = MaxResourcePercentage
		}
	}
	if _, ok := thresholds[v1.ResourceCPU]; !ok {
		if useDeviationThresholds {
			thresholds[v1.ResourceCPU] = MinResourcePercentage
			targetThresholds[v1.ResourceCPU] = MinResourcePercentage
		} else {
			thresholds[v1.ResourceCPU] = MaxResourcePercentage
			targetThresholds[v1.ResourceCPU] = MaxResourcePercentage
		}
	}
	if _, ok := thresholds[v1.ResourceMemory]; !ok {
		if useDeviationThresholds {
			thresholds[v1.ResourceMemory] = MinResourcePercentage
			targetThresholds[v1.ResourceMemory] = MinResourcePercentage
		} else {
			thresholds[v1.ResourceMemory] = MaxResourcePercentage
			targetThresholds[v1.ResourceMemory] = MaxResourcePercentage
		}
	}

	resourceNames := getResourceNames(thresholds)

	lowNodes, sourceNodes := classifyNodes(
		h.getNodeUsage(nodes, resourceNames, h.handle.GetPodsAssignedToNodeFunc()),
		NodeThresholds{
			lowResourceThreshold:  thresholds,
			highResourceThreshold: targetThresholds,
		},
		func(node *v1.Node, usage NodeUsage, threshold NodeThresholds) bool {
			return isNodeWithLowUtilization(usage, threshold.lowResourceThreshold)
		},
		func(node *v1.Node, usage NodeUsage, threshold NodeThresholds) bool {
			if nodeutil.IsNodeUnschedulable(node) {
				klog.V(2).InfoS("Node is unschedulable", "node", klog.KObj(node))
				return false
			}
			return !isNodeWithLowUtilization(usage, threshold.lowResourceThreshold)
		})
	if len(sourceNodes) == 0 {
		klog.V(1).InfoS("No node is underutilized, nothing to do here, you might tune your thresholds further")
		return nil
	}
	if len(sourceNodes) <= h.args.NumberOfNodes {
		klog.V(1).InfoS("Number of nodes underutilized is less or equal than NumberOfNodes, nothing to do here", "underutilizedNodes", len(sourceNodes), "numberOfNodes", h.args.NumberOfNodes)
		return nil
	}
	if len(sourceNodes) == len(nodes) {
		klog.V(1).InfoS("All nodes are underutilized, nothing to do here")
		return nil
	}
	if len(lowNodes) == 0 {
		klog.V(1).InfoS("No node is available to schedule the pods, nothing to do here")
		return nil
	}

	// stop if the total available usage has dropped to zero - no more pods can be scheduled
	continueEvictionCond := func(nodeInfo NodeInfo, totalAvailableUsage map[v1.ResourceName]*resource.Quantity) bool {
		for name := range totalAvailableUsage {
			if totalAvailableUsage[name].CmpInt64(0) < 1 {
				return false
			}
		}

		return true
	}

	// Sort the nodes by the usage in ascending order
	sortNodesByUsage(sourceNodes, true)

	evictPodsFromSourceNodes(
		ctx,
		NodeThresholds{
			lowResourceThreshold:  thresholds,
			highResourceThreshold: targetThresholds,
		},
		h.args.EvictableNamespaces,
		lowNodes,
		sourceNodes,
		h.handle.Evictor(),
		h.podFilter,
		resourceNames,
		continueEvictionCond)

	return nil
}

// isNodeWithLowUtilization checks if a node is underutilized
// All resources have to be below the low threshold
func isNodeWithLowUtilization(usage NodeUsage, threshold api.ResourceThresholds) bool {
	return usage.cpuUsageAvg < float64(threshold[v1.ResourceCPU]) && usage.memUsageAvg < float64(threshold[v1.ResourceMemory])
}

// evictPodsFromSourceNodes evicts pods based on priority, if all the pods on the node have priority, if not
// evicts them based on QoS as fallback option.
// TODO: @ravig Break this function into smaller functions.
func evictPodsFromSourceNodes(
	ctx context.Context,
	nodeThresholds NodeThresholds,
	evictableNamespaces *api.Namespaces,
	sourceNodes, destinationNodes []NodeInfo,
	podEvictor frameworktypes.Evictor,
	podFilter func(pod *v1.Pod) bool,
	resourceNames []v1.ResourceName,
	continueEviction continueEvictionCond,
) {
	// upper bound on total number of pods/cpu/memory and optional extended resources to be moved
	totalAvailableUsage := map[v1.ResourceName]*resource.Quantity{
		v1.ResourcePods:   {},
		v1.ResourceCPU:    {},
		v1.ResourceMemory: {},
	}

	taintsOfDestinationNodes := make(map[string][]v1.Taint, len(destinationNodes))
	for _, node := range destinationNodes {
		taintsOfDestinationNodes[node.node.Name] = node.node.Spec.Taints

		for _, name := range resourceNames {
			if _, ok := totalAvailableUsage[name]; !ok {
				totalAvailableUsage[name] = resource.NewQuantity(0, resource.DecimalSI)
			}
			totalAvailableUsage[name].Add(node.node.Status.Allocatable[name]) // resource.NewQuantity(int64(nodeThresholds.highResourceThreshold[name]),64) *
			totalAvailableUsage[name].Sub(*node.usage[name])
		}
	}

	for _, node := range sourceNodes {
		klog.V(3).InfoS("Evicting pods from node", "node", klog.KObj(node.node), "usage", node.usage)

		nonRemovablePods, removablePods := classifyPods(node.allPods, podFilter)
		klog.V(2).InfoS("Pods on node", "node", klog.KObj(node.node), "allPods", len(node.allPods), "nonRemovablePods", len(nonRemovablePods), "removablePods", len(removablePods))

		if len(removablePods) == 0 {
			klog.V(1).InfoS("No removable pods on node, try next node", "node", klog.KObj(node.node))
			continue
		}

		klog.V(1).InfoS("Evicting pods based on priority, if they have same priority, they'll be evicted based on QoS tiers")
		// sort the evictable Pods based on priority. This also sorts them based on QoS. If there are multiple pods with same priority, they are sorted based on QoS tiers.
		podutil.SortPodsBasedOnPriorityLowToHigh(removablePods)
		evictPods(ctx, evictableNamespaces, removablePods, node, totalAvailableUsage, taintsOfDestinationNodes, podEvictor, continueEviction)
	}
}

func classifyPods(pods []*v1.Pod, filter func(pod *v1.Pod) bool) ([]*v1.Pod, []*v1.Pod) {
	var nonRemovablePods, removablePods []*v1.Pod
	for _, pod := range pods {
		if !filter(pod) {
			nonRemovablePods = append(nonRemovablePods, pod)
		} else {
			removablePods = append(removablePods, pod)
		}
	}

	return nonRemovablePods, removablePods
}

func evictPods(
	ctx context.Context,
	evictableNamespaces *api.Namespaces,
	inputPods []*v1.Pod,
	nodeInfo NodeInfo,
	totalAvailableUsage map[v1.ResourceName]*resource.Quantity,
	taintsOfLowNodes map[string][]v1.Taint,
	podEvictor frameworktypes.Evictor,
	continueEviction continueEvictionCond,
) {
	var excludedNamespaces sets.Set[string]
	if evictableNamespaces != nil {
		excludedNamespaces = sets.New(evictableNamespaces.Exclude...)
	}

	if continueEviction(nodeInfo, totalAvailableUsage) {
		for _, pod := range inputPods {
			if !utils.PodToleratesTaints(pod, taintsOfLowNodes) {
				klog.V(3).InfoS("Skipping eviction for pod, doesn't tolerate node taint", "pod", klog.KObj(pod))
				continue
			}

			preEvictionFilterWithOptions, err := podutil.NewOptions().
				WithFilter(podEvictor.PreEvictionFilter).
				WithoutNamespaces(excludedNamespaces).
				BuildFilterFunc()
			if err != nil {
				klog.ErrorS(err, "could not build preEvictionFilter with namespace exclusion")
				continue
			}

			if preEvictionFilterWithOptions(pod) {
				if podEvictor.Evict(ctx, pod, evictions.EvictOptions{}) {
					klog.V(3).InfoS("Evicted pods", "pod", klog.KObj(pod))

					for name := range totalAvailableUsage {
						if name == v1.ResourcePods {
							nodeInfo.usage[name].Sub(*resource.NewQuantity(1, resource.DecimalSI))
							totalAvailableUsage[name].Sub(*resource.NewQuantity(1, resource.DecimalSI))
						} else {
							quantity := utils.GetResourceRequestQuantity(pod, name)
							nodeInfo.usage[name].Sub(quantity)
							totalAvailableUsage[name].Sub(quantity)
						}
					}

					keysAndValues := []interface{}{
						"node", nodeInfo.node.Name,
						"CPU", nodeInfo.usage[v1.ResourceCPU].MilliValue(),
						"Mem", nodeInfo.usage[v1.ResourceMemory].Value(),
						"Pods", nodeInfo.usage[v1.ResourcePods].Value(),
					}
					for name := range totalAvailableUsage {
						if !nodeutil.IsBasicResource(name) {
							keysAndValues = append(keysAndValues, string(name), totalAvailableUsage[name].Value())
						}
					}

					klog.V(3).InfoS("Updated node usage", keysAndValues...)
					// check if pods can be still evicted
					if !continueEviction(nodeInfo, totalAvailableUsage) {
						break
					}
				}
			}
			if podEvictor.NodeLimitExceeded(nodeInfo.node) {
				return
			}
		}
	}
}

// sortNodesByUsage sorts nodes based on usage according to the given plugin.
func sortNodesByUsage(nodes []NodeInfo, ascending bool) {
	sort.Slice(nodes, func(i, j int) bool {
		ti := nodes[i].usage[v1.ResourceMemory].Value() + nodes[i].usage[v1.ResourceCPU].MilliValue() + nodes[i].usage[v1.ResourcePods].Value()
		tj := nodes[j].usage[v1.ResourceMemory].Value() + nodes[j].usage[v1.ResourceCPU].MilliValue() + nodes[j].usage[v1.ResourcePods].Value()

		// extended resources
		for name := range nodes[i].usage {
			if !nodeutil.IsBasicResource(name) {
				ti = ti + nodes[i].usage[name].Value()
				tj = tj + nodes[j].usage[name].Value()
			}
		}

		// Return ascending order for HighNodeUtilization plugin
		if ascending {
			return ti < tj
		}

		// Return descending order for LowNodeUtilization plugin
		return ti > tj
	})
}

func (h *LoadAware) getNodeUsage(
	nodes []*v1.Node,
	resourceNames []v1.ResourceName,
	getPodsAssignedToNode podutil.GetPodsAssignedToNodeFunc,
) map[string]*NodeUsage {
	nodeUsages := map[string]*NodeUsage{}
	for _, v := range nodes {
		pods, err := podutil.ListPodsOnANode(v.Name, getPodsAssignedToNode, nil)
		if err != nil {
			klog.ErrorS(err, "Node will not be processed, error accessing its pods", "node", klog.KObj(v))
			continue
		}
		client, err := NewMetricsClient(h.args.MetricsConfiguration)
		if err != nil {
			klog.Errorf("Error creating client: %v\n", err)
		}
		nodeMetric, err := client.NodeMetricsAvg(context.TODO(), v.Name, h.args.MetricsConfiguration.Interval)
		if err != nil {
			klog.ErrorS(err, "Failed to get NodeMetric", "node", klog.KObj(v))
			continue
		}
		nodeUsages[v.Name] = &NodeUsage{
			node:        v,
			cpuUsageAvg: nodeMetric.CPU,
			memUsageAvg: nodeMetric.Memory,
			allPods:     pods,
		}
	}

	return nodeUsages
}

// getResourceNames returns list of resource names in resource thresholds
func getResourceNames(thresholds api.ResourceThresholds) []v1.ResourceName {
	resourceNames := make([]v1.ResourceName, 0, len(thresholds))
	for name := range thresholds {
		resourceNames = append(resourceNames, name)
	}
	return resourceNames
}

// classifyNodes classifies the nodes into low-utilization or high-utilization nodes. If a node lies between
// low and high thresholds, it is simply ignored.
func classifyNodes(
	nodeUsages map[string]*NodeUsage,
	nodeThresholds NodeThresholds,
	lowThresholdFilter, highThresholdFilter func(node *v1.Node, usage NodeUsage, threshold NodeThresholds) bool,
) ([]NodeInfo, []NodeInfo) {
	lowNodes, highNodes := []NodeInfo{}, []NodeInfo{}

	for _, nodeUsage := range nodeUsages {
		nodeInfo := NodeInfo{
			NodeUsage: *nodeUsage,
		}
		if lowThresholdFilter(nodeUsage.node, *nodeUsage, nodeThresholds) {
			klog.InfoS("Node is underutilized", "node", klog.KObj(nodeUsage.node), "usage", nodeUsage.usage, "usagePercentage", resourceUsagePercentages(*nodeUsage))
			lowNodes = append(lowNodes, nodeInfo)
		} else if highThresholdFilter(nodeUsage.node, *nodeUsage, nodeThresholds) {
			klog.InfoS("Node is overutilized", "node", klog.KObj(nodeUsage.node), "usage", nodeUsage.usage, "usagePercentage", resourceUsagePercentages(*nodeUsage))
			highNodes = append(highNodes, nodeInfo)
		} else {
			klog.InfoS("Node is appropriately utilized", "node", klog.KObj(nodeUsage.node), "usage", nodeUsage.usage, "usagePercentage", resourceUsagePercentages(*nodeUsage))
		}
	}

	return lowNodes, highNodes
}

func resourceUsagePercentages(nodeUsage NodeUsage) map[v1.ResourceName]float64 {
	nodeCapacity := nodeUsage.node.Status.Capacity
	if len(nodeUsage.node.Status.Allocatable) > 0 {
		nodeCapacity = nodeUsage.node.Status.Allocatable
	}

	resourceUsagePercentage := map[v1.ResourceName]float64{}
	for resourceName, resourceUsage := range nodeUsage.usage {
		cap := nodeCapacity[resourceName]
		if !cap.IsZero() {
			resourceUsagePercentage[resourceName] = 100 * float64(resourceUsage.MilliValue()) / float64(cap.MilliValue())
		}
	}

	return resourceUsagePercentage
}
