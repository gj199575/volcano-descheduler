/*
Copyright 2017 The Kubernetes Authors.

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

package evictions

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	v1 "k8s.io/api/core/v1"
	policy "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	"sigs.k8s.io/descheduler/metrics"
	deevictions "sigs.k8s.io/descheduler/pkg/descheduler/evictions"

	eutils "sigs.k8s.io/descheduler/pkg/descheduler/evictions/utils"
	"sigs.k8s.io/descheduler/pkg/tracing"
)

// nodePodEvictedCount keeps count of pods evicted on node
type (
	nodePodEvictedCount    map[string]uint
	namespacePodEvictCount map[string]uint
)

type PodEvictor struct {
	*deevictions.PodEvictor
	client                     clientset.Interface
	nodes                      []*v1.Node
	policyGroupVersion         string
	dryRun                     bool
	maxPodsToEvictPerNode      *uint
	maxPodsToEvictPerNamespace *uint
	nodepodCount               nodePodEvictedCount
	namespacePodCount          namespacePodEvictCount
	metricsEnabled             bool
	eventRecorder              events.EventRecorder
}

func NewPodEvictor(
	client clientset.Interface,
	policyGroupVersion string,
	dryRun bool,
	maxPodsToEvictPerNode *uint,
	maxPodsToEvictPerNamespace *uint,
	nodes []*v1.Node,
	metricsEnabled bool,
	eventRecorder events.EventRecorder,
) *PodEvictor {
	evictor := deevictions.NewPodEvictor(client, policyGroupVersion, dryRun, maxPodsToEvictPerNode, maxPodsToEvictPerNamespace, nodes, metricsEnabled, eventRecorder)
	nodePodCount := make(nodePodEvictedCount)
	namespacePodCount := make(namespacePodEvictCount)
	for _, node := range nodes {
		// Initialize podsEvicted till now with 0.
		nodePodCount[node.Name] = 0
	}
	return &PodEvictor{
		PodEvictor:                 evictor,
		client:                     client,
		nodes:                      nodes,
		policyGroupVersion:         policyGroupVersion,
		dryRun:                     dryRun,
		maxPodsToEvictPerNode:      maxPodsToEvictPerNode,
		maxPodsToEvictPerNamespace: maxPodsToEvictPerNamespace,
		nodepodCount:               nodePodCount,
		namespacePodCount:          namespacePodCount,
		metricsEnabled:             metricsEnabled,
		eventRecorder:              eventRecorder,
	}
}

// EvictOptions provides a handle for passing additional info to EvictPod
type EvictOptions struct {
	// Reason allows for passing details about the specific eviction for logging.
	Reason string
}

// EvictPod evicts a pod while exercising eviction limits.
// Returns true when the pod is evicted on the server side.
func (pe *PodEvictor) EvictPod(ctx context.Context, pod *v1.Pod, opts deevictions.EvictOptions) bool {
	var span trace.Span
	ctx, span = tracing.Tracer().Start(ctx, "EvictPod", trace.WithAttributes(attribute.String("podName", pod.Name), attribute.String("podNamespace", pod.Namespace), attribute.String("reason", opts.Reason), attribute.String("operation", tracing.EvictOperation)))
	defer span.End()
	// TODO: Replace context-propagated Strategy name with a defined framework handle for accessing Strategy info
	strategy := ""
	if ctx.Value("strategyName") != nil {
		strategy = ctx.Value("strategyName").(string)
	}

	if pod.Spec.NodeName != "" {
		if pe.maxPodsToEvictPerNode != nil && pe.nodepodCount[pod.Spec.NodeName]+1 > *pe.maxPodsToEvictPerNode {
			if pe.metricsEnabled {
				metrics.PodsEvicted.With(map[string]string{"result": "maximum number of pods per node reached", "strategy": strategy, "namespace": pod.Namespace, "node": pod.Spec.NodeName}).Inc()
			}
			span.AddEvent("Eviction Failed", trace.WithAttributes(attribute.String("node", pod.Spec.NodeName), attribute.String("err", "Maximum number of evicted pods per node reached")))
			klog.ErrorS(fmt.Errorf("Maximum number of evicted pods per node reached"), "limit", *pe.maxPodsToEvictPerNode, "node", pod.Spec.NodeName)
			return false
		}
	}

	if pe.maxPodsToEvictPerNamespace != nil && pe.namespacePodCount[pod.Namespace]+1 > *pe.maxPodsToEvictPerNamespace {
		if pe.metricsEnabled {
			metrics.PodsEvicted.With(map[string]string{"result": "maximum number of pods per namespace reached", "strategy": strategy, "namespace": pod.Namespace, "node": pod.Spec.NodeName}).Inc()
		}
		span.AddEvent("Eviction Failed", trace.WithAttributes(attribute.String("node", pod.Spec.NodeName), attribute.String("err", "Maximum number of evicted pods per namespace reached")))
		klog.ErrorS(fmt.Errorf("Maximum number of evicted pods per namespace reached"), "limit", *pe.maxPodsToEvictPerNamespace, "namespace", pod.Namespace)
		return false
	}

	err := evictPod(ctx, pe.client, pod, pe.policyGroupVersion)
	if err != nil {
		// err is used only for logging purposes
		span.AddEvent("Eviction Failed", trace.WithAttributes(attribute.String("node", pod.Spec.NodeName), attribute.String("err", err.Error())))
		klog.ErrorS(err, "Error evicting pod", "pod", klog.KObj(pod), "reason", opts.Reason)
		if pe.metricsEnabled {
			metrics.PodsEvicted.With(map[string]string{"result": "error", "strategy": strategy, "namespace": pod.Namespace, "node": pod.Spec.NodeName}).Inc()
		}
		return false
	}

	if pod.Spec.NodeName != "" {
		pe.nodepodCount[pod.Spec.NodeName]++
	}
	pe.namespacePodCount[pod.Namespace]++

	if pe.metricsEnabled {
		metrics.PodsEvicted.With(map[string]string{"result": "success", "strategy": strategy, "namespace": pod.Namespace, "node": pod.Spec.NodeName}).Inc()
	}

	if pe.dryRun {
		klog.V(1).InfoS("Evicted pod in dry run mode", "pod", klog.KObj(pod), "reason", opts.Reason, "strategy", strategy, "node", pod.Spec.NodeName)
	} else {
		klog.V(1).InfoS("Evicted pod", "pod", klog.KObj(pod), "reason", opts.Reason, "strategy", strategy, "node", pod.Spec.NodeName)
		reason := opts.Reason
		if len(reason) == 0 {
			reason = strategy
			if len(reason) == 0 {
				reason = "NotSet"
			}
		}
		pe.eventRecorder.Eventf(pod, nil, v1.EventTypeNormal, reason, "Descheduled", "pod evicted from %v node by sigs.k8s.io/descheduler", pod.Spec.NodeName)
	}
	return true
}

func evictPod(ctx context.Context, client clientset.Interface, pod *v1.Pod, policyGroupVersion string) error {

	fakePod := NewFakePod(pod)
	createOptions := metav1.CreateOptions{}
	create, err2 := client.CoreV1().Pods(pod.Namespace).Create(ctx, fakePod, createOptions)
	fmt.Println(create, err2)

	getOptions := metav1.GetOptions{}
	for {
		getPod, getError := client.CoreV1().Pods(fakePod.Namespace).Get(ctx, fakePod.Name, getOptions)
		fmt.Println(getPod, getError)
		time.Sleep(time.Second)
		if getPod.Annotations[FakePod] != "" {
			deleteError := client.CoreV1().Pods(fakePod.Namespace).Delete(ctx, fakePod.Name, metav1.DeleteOptions{})
			fmt.Println(deleteError)
			break
		}
	}
	pod.Annotations["scheduling.k8s.io/group-name"] = "job1-e7f18111-1cec-11ea-b688-fa163ec79500"
	update, err2 := client.CoreV1().Pods(pod.Namespace).Update(context.TODO(), pod, metav1.UpdateOptions{})
	fmt.Println(update, err2)

	deleteOptions := &metav1.DeleteOptions{}
	// GracePeriodSeconds ?
	eviction := &policy.Eviction{
		TypeMeta: metav1.TypeMeta{
			APIVersion: policyGroupVersion,
			Kind:       eutils.EvictionKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
		DeleteOptions: deleteOptions,
	}
	err := client.PolicyV1().Evictions(eviction.Namespace).Evict(ctx, eviction)

	if apierrors.IsTooManyRequests(err) {
		return fmt.Errorf("error when evicting pod (ignoring) %q: %v", pod.Name, err)
	}
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("pod not found when evicting %q: %v", pod.Name, err)
	}
	return err
}

const IsFake = "is-fake"
const FakePod = "fake-pod"

func NewFakePod(pod1 *v1.Pod) *v1.Pod {
	pod := pod1.DeepCopy()
	pod.Labels = nil
	pod.Spec.NodeName = ""
	pod.Spec.SchedulerName = "volcano"
	pod.OwnerReferences = nil
	pod.ResourceVersion = ""
	pod.Name = pod.Name + "-fake"
	pod.Annotations[IsFake] = "true"
	return pod
}

func EvictPod(ctx context.Context, client clientset.Interface, pod *v1.Pod, policyGroupVersion string) {
	evictPod(ctx, client, pod, policyGroupVersion)
}
