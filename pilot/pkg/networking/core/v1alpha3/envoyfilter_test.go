// Copyright 2018 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha3

import (
	"net"
	"reflect"
	"testing"

	xdsapi "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/listener"
	"github.com/gogo/protobuf/types"

	networking "istio.io/api/networking/v1alpha3"
	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pilot/pkg/networking/core/v1alpha3/fakes"
	"istio.io/istio/pilot/pkg/networking/plugin"
)

func buildEnvoyFilterConfigStore(listeners []*networking.EnvoyFilter_Listener) *fakes.IstioConfigStore {
	return &fakes.IstioConfigStore{
		EnvoyFilterStub: func(workloadLabels model.LabelsCollection) *model.Config {
			return &model.Config{
				ConfigMeta: model.ConfigMeta{
					Name:      "test-envoyfilter",
					Namespace: "not-default",
				},
				Spec: &networking.EnvoyFilter{
					Listeners: listeners,
				},
			}
		},
	}

}

func TestApplyUserListenerConfig(t *testing.T) {
	serviceDiscovery := &fakes.ServiceDiscovery{}
	listenerConfig := `{"address": { "pipe": { "path": "some-path" } }, "filter_chains": [{"filters": [{"name": "envoy.ratelimit"}]}]}`
	addPatchWithNoPath := []*networking.EnvoyFilter_Listener{
		{
			Patches: []*networking.EnvoyFilter_Patch{
				{
					Operator: networking.EnvoyFilter_Patch_ADD,
					Value: &types.Value{
						Kind: &types.Value_StringValue{StringValue: listenerConfig},
					},
				},
			},
		},
	}

	mergePatch := []*networking.EnvoyFilter_Listener{
		{
			Patches: []*networking.EnvoyFilter_Patch{
				{
					// TODO: figure out why jsonpb isn't unmarshaling to snake case
					// Path:     `{range .filterChains[*]}{.filters[?(@.name == "envoy.ratelimit")]}`,
					Path: `/filterChains/0/filters/name=envoy.ratelimit/name`,
					Operator: networking.EnvoyFilter_Patch_MERGE,
					Value: &types.Value{
						Kind: &types.Value_StringValue{StringValue: "envoy.http_connection_manager"},
					},
				},
			},
		},
	}

	testCases := []struct {
		name      string
		listeners []*xdsapi.Listener
		env       *model.Environment
		labels    model.LabelsCollection
		result    []*xdsapi.Listener
	}{
		{
			name:      "listener config happy path",
			listeners: make([]*xdsapi.Listener, 0),
			env:       newTestEnvironment(serviceDiscovery, testMesh, buildEnvoyFilterConfigStore(addPatchWithNoPath)),
			labels:    model.LabelsCollection{},
			result: []*xdsapi.Listener{
				{
					Address: core.Address{
						Address: &core.Address_Pipe{
							Pipe: &core.Pipe{
								Path: "some-path",
							},
						},
					},
					FilterChains: []listener.FilterChain{
						{
							Filters: []listener.Filter{
								{
									Name: "envoy.ratelimit",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "merge with jsonpath",
			listeners: []*xdsapi.Listener{
				{
					Address: core.Address{
						Address: &core.Address_Pipe{
							Pipe: &core.Pipe{
								Path: "some-path",
							},
						},
					},
					FilterChains: []listener.FilterChain{
						{
							Filters: []listener.Filter{
								{
									Name: "envoy.ratelimit",
								},
							},
						},
					},
				},
			},
			env:    newTestEnvironment(serviceDiscovery, testMesh, buildEnvoyFilterConfigStore(mergePatch)),
			labels: model.LabelsCollection{},
			result: []*xdsapi.Listener{
				{
					Address: core.Address{
						Address: &core.Address_Pipe{
							Pipe: &core.Pipe{
								Path: "some-path",
							},
						},
					},
					FilterChains: []listener.FilterChain{
						{
							Filters: []listener.Filter{
								{
									Name: "envoy.http_connection_manager",
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		ret := applyUserListenerConfig(tc.listeners, tc.env, tc.labels)
		if !reflect.DeepEqual(tc.result, ret) {
			t.Errorf("test case %s: expecting %v but got %v", tc.name, tc.result, ret)
		}
	}
}

func TestListenerMatch(t *testing.T) {
	inputParams := &plugin.InputParams{
		ListenerProtocol: plugin.ListenerProtocolHTTP,
		Node: &model.Proxy{
			Type: model.SidecarProxy,
		},
		Port: &model.Port{
			Name: "http-foo",
			Port: 80,
		},
	}

	testCases := []struct {
		name           string
		inputParams    *plugin.InputParams
		listenerIP     net.IP
		matchCondition *networking.EnvoyFilter_ListenerMatch
		direction      networking.EnvoyFilter_ListenerMatch_ListenerType
		result         bool
	}{
		{
			name:        "empty match",
			inputParams: inputParams,
			result:      true,
		},
		{
			name:           "match by port",
			inputParams:    inputParams,
			matchCondition: &networking.EnvoyFilter_ListenerMatch{PortNumber: 80},
			result:         true,
		},
		{
			name:           "match by port name prefix",
			inputParams:    inputParams,
			matchCondition: &networking.EnvoyFilter_ListenerMatch{PortNamePrefix: "http"},
			result:         true,
		},
		{
			name:           "match by listener type",
			inputParams:    inputParams,
			direction:      networking.EnvoyFilter_ListenerMatch_SIDECAR_OUTBOUND,
			matchCondition: &networking.EnvoyFilter_ListenerMatch{ListenerType: networking.EnvoyFilter_ListenerMatch_SIDECAR_OUTBOUND},
			result:         true,
		},
		{
			name:           "match by listener protocol",
			inputParams:    inputParams,
			matchCondition: &networking.EnvoyFilter_ListenerMatch{ListenerProtocol: networking.EnvoyFilter_ListenerMatch_HTTP},
			result:         true,
		},
		{
			name:           "match by listener address with CIDR",
			inputParams:    inputParams,
			listenerIP:     net.ParseIP("10.10.10.10"),
			matchCondition: &networking.EnvoyFilter_ListenerMatch{Address: []string{"10.10.10.10/24", "192.168.0.1/24"}},
			result:         true,
		},
		{
			name:        "match outbound sidecar http listeners on 10.10.10.0/24:80, with port name prefix http-*",
			inputParams: inputParams,
			listenerIP:  net.ParseIP("10.10.10.10"),
			direction:   networking.EnvoyFilter_ListenerMatch_SIDECAR_OUTBOUND,
			matchCondition: &networking.EnvoyFilter_ListenerMatch{
				PortNumber:       80,
				PortNamePrefix:   "http",
				ListenerType:     networking.EnvoyFilter_ListenerMatch_SIDECAR_OUTBOUND,
				ListenerProtocol: networking.EnvoyFilter_ListenerMatch_HTTP,
				Address:          []string{"10.10.10.0/24"},
			},
			result: true,
		},
		{
			name:        "does not match: outbound sidecar http listeners on 10.10.10.0/24:80, with port name prefix tcp-*",
			inputParams: inputParams,
			listenerIP:  net.ParseIP("10.10.10.10"),
			direction:   networking.EnvoyFilter_ListenerMatch_SIDECAR_OUTBOUND,
			matchCondition: &networking.EnvoyFilter_ListenerMatch{
				PortNumber:       80,
				PortNamePrefix:   "tcp",
				ListenerType:     networking.EnvoyFilter_ListenerMatch_SIDECAR_OUTBOUND,
				ListenerProtocol: networking.EnvoyFilter_ListenerMatch_HTTP,
				Address:          []string{"10.10.10.0/24"},
			},
			result: false,
		},
		{
			name:        "does not match: inbound sidecar http listeners with port name prefix http-*",
			inputParams: inputParams,
			direction:   networking.EnvoyFilter_ListenerMatch_SIDECAR_OUTBOUND,
			matchCondition: &networking.EnvoyFilter_ListenerMatch{
				PortNamePrefix:   "http",
				ListenerType:     networking.EnvoyFilter_ListenerMatch_SIDECAR_INBOUND,
				ListenerProtocol: networking.EnvoyFilter_ListenerMatch_HTTP,
			},
			result: false,
		},
		{
			name:        "does not match: outbound gateway http listeners on 10.10.10.0/24:80, with port name prefix http-*",
			inputParams: inputParams,
			listenerIP:  net.ParseIP("10.10.10.10"),
			direction:   networking.EnvoyFilter_ListenerMatch_SIDECAR_OUTBOUND,
			matchCondition: &networking.EnvoyFilter_ListenerMatch{
				PortNumber:       80,
				PortNamePrefix:   "http",
				ListenerType:     networking.EnvoyFilter_ListenerMatch_GATEWAY,
				ListenerProtocol: networking.EnvoyFilter_ListenerMatch_HTTP,
				Address:          []string{"10.10.10.0/24"},
			},
			result: false,
		},
		{
			name:        "does not match: outbound sidecar listeners on 172.16.0.1/16:80, with port name prefix http-*",
			inputParams: inputParams,
			listenerIP:  net.ParseIP("10.10.10.10"),
			direction:   networking.EnvoyFilter_ListenerMatch_SIDECAR_OUTBOUND,
			matchCondition: &networking.EnvoyFilter_ListenerMatch{
				PortNumber:       80,
				PortNamePrefix:   "http",
				ListenerType:     networking.EnvoyFilter_ListenerMatch_SIDECAR_OUTBOUND,
				ListenerProtocol: networking.EnvoyFilter_ListenerMatch_HTTP,
				Address:          []string{"172.16.0.1/16"},
			},
			result: false,
		},
	}

	for _, tc := range testCases {
		tc.inputParams.ListenerCategory = tc.direction
		ret := listenerMatch(tc.inputParams, tc.listenerIP, tc.matchCondition)
		if tc.result != ret {
			t.Errorf("%s: expecting %v but got %v", tc.name, tc.result, ret)
		}
	}
}
