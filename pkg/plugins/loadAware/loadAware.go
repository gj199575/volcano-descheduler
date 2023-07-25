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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/descheduler/pkg/descheduler"
	podutil "sigs.k8s.io/descheduler/pkg/descheduler/pod"
	"sigs.k8s.io/descheduler/pkg/framework/pluginregistry"
	"sigs.k8s.io/descheduler/pkg/framework/types"
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
	podFilter, err := podutil.NewOptions().
		WithFilter(handle.Evictor().Filter).
		BuildFilterFunc()
	if err != nil {
		return nil, fmt.Errorf("error initializing pod filter function: %v", err)
	}

	return &LoadAware{
		handle:    handle,
		args:      nil,
		podFilter: podFilter,
	}, nil
}

// Name retrieves the plugin name
func (h *LoadAware) Name() string {
	return LoadAwarePluginName
}

// Balance extension point implementation for the plugin
func (h *LoadAware) Balance(ctx context.Context, nodes []*v1.Node) *types.Status {
	return nil
}
