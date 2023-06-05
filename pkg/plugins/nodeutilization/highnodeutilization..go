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

package nodeutilization

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/descheduler/cmd/descheduler/app"
	"sigs.k8s.io/descheduler/pkg/descheduler"
	podutil "sigs.k8s.io/descheduler/pkg/descheduler/pod"
	"sigs.k8s.io/descheduler/pkg/framework/pluginregistry"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/defaultevictor"
	"sigs.k8s.io/descheduler/pkg/framework/types"
)

func init() {
	app.SetupLogs()
	descheduler.SetupPlugins()
	fmt.Println("123")
	pluginregistry.Register(HighNodeUtilizationPluginName, NewHighNodeUtilization, &defaultevictor.DefaultEvictor{}, &defaultevictor.DefaultEvictorArgs{}, nil, nil, pluginregistry.PluginRegistry)
}

const HighNodeUtilizationPluginName = "HighNodeUtilization"

// HighNodeUtilization evicts pods from under utilized nodes so that scheduler can schedule according to its plugin.
// Note that CPU/Memory requests are used to calculate nodes' utilization and not the actual resource usage.

type HighNodeUtilization struct {
	handle    types.Handle
	args      *HighNodeUtilizationArgs
	podFilter func(pod *v1.Pod) bool
}

var _ types.BalancePlugin = &HighNodeUtilization{}

// NewHighNodeUtilization builds plugin from its arguments while passing a handle
func NewHighNodeUtilization(args runtime.Object, handle types.Handle) (types.Plugin, error) {

	podFilter, err := podutil.NewOptions().
		WithFilter(handle.Evictor().Filter).
		BuildFilterFunc()
	if err != nil {
		return nil, fmt.Errorf("error initializing pod filter function: %v", err)
	}

	return &HighNodeUtilization{
		handle:    handle,
		args:      nil,
		podFilter: podFilter,
	}, nil
}

// Name retrieves the plugin name
func (h *HighNodeUtilization) Name() string {
	return HighNodeUtilizationPluginName
}

// Balance extension point implementation for the plugin
func (h *HighNodeUtilization) Balance(ctx context.Context, nodes []*v1.Node) *types.Status {

	return nil
}
