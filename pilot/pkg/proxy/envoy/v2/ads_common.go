// Copyright 2019 Istio Authors
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

package v2

import (
	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pkg/config/host"
	"istio.io/istio/pkg/config/schema/collections"
	"istio.io/istio/pkg/config/schema/resource"
)

var (
	pushResourceScope = map[resource.GroupVersionKind]func(proxy *model.Proxy, pushEv *XdsEvent, resources map[string]struct{}) bool{
		model.ServiceEntryKind:    serviceEntryAffectProxy,
		model.VirtualServiceKind:  virtualServiceAffectProxy,
		model.DestinationRuleKind: destinationRuleAffectProxy,
	}
)

func serviceEntryAffectProxy(proxy *model.Proxy, pushEv *XdsEvent, resources map[string]struct{}) bool {
	_ = pushEv
	if len(resources) == 0 {
		return true
	}

	for name := range resources {
		if proxy.SidecarScope.DependsOnService(host.Name(name)) {
			return true
		}
	}
	return false
}

func virtualServiceAffectProxy(proxy *model.Proxy, pushEv *XdsEvent, resources map[string]struct{}) bool {
	_ = pushEv
	if len(resources) == 0 {
		return true
	}

	for name := range resources {
		if proxy.SidecarScope.DependsOnVirtualService(name) {
			return true
		}
	}
	return false
}

func destinationRuleAffectProxy(proxy *model.Proxy, pushEv *XdsEvent, resources map[string]struct{}) bool {
	_ = pushEv
	if len(resources) == 0 {
		return true
	}

	for name := range resources {
		if proxy.SidecarScope.DependsOnDestinationRule(name) {
			return true
		}
	}
	return false
}

func PushAffectProxy(pushEv *XdsEvent, proxy *model.Proxy) bool {
	if len(pushEv.configsUpdated) == 0 {
		return true
	}

	for kind, resources := range pushEv.configsUpdated {
		if scope, f := pushResourceScope[kind]; !f {
			return true
		} else if scope(proxy, pushEv, resources) {
			return true
		}
	}

	return false
}

func ProxyNeedsPush(proxy *model.Proxy, pushEv *XdsEvent) bool {
	targetNamespaces := pushEv.namespacesUpdated
	configs := pushEv.configsUpdated

	// appliesToProxy starts as false, we will set it to true if we encounter any configs that require a push
	appliesToProxy := false
	// If no config specified, this request applies to all proxies
	if len(configs) == 0 {
		appliesToProxy = true
	}
Loop:
	for config := range configs {
		switch config {
		case collections.IstioNetworkingV1Alpha3Gateways.Resource().GroupVersionKind():
			if proxy.Type == model.Router {
				return true
			}
		case collections.IstioMixerV1ConfigClientQuotaspecs.Resource().GroupVersionKind(),
			collections.IstioMixerV1ConfigClientQuotaspecbindings.Resource().GroupVersionKind():
			if proxy.Type == model.SidecarProxy {
				return true
			}
		default:
			appliesToProxy = true
			break Loop
		}
	}

	if appliesToProxy {
		appliesToProxy = PushAffectProxy(pushEv, proxy)
	}

	if !appliesToProxy {
		return false
	}

	// If no only namespaces specified, this request applies to all proxies
	if len(targetNamespaces) == 0 {
		return true
	}

	// If the proxy's service updated, need push for it.
	if len(proxy.ServiceInstances) > 0 {
		ns := proxy.ServiceInstances[0].Service.Attributes.Namespace
		if _, ok := targetNamespaces[ns]; ok {
			return true
		}
	}

	// Otherwise, only apply if the egress listener will import the config present in the update
	for ns := range targetNamespaces {
		if proxy.SidecarScope.DependsOnNamespace(ns) {
			return true
		}
	}
	return false
}

type XdsType int

const (
	CDS XdsType = iota
	EDS
	LDS
	RDS
)

// TODO: merge with ProxyNeedsPush
func PushTypeFor(proxy *model.Proxy, pushEv *XdsEvent) map[XdsType]bool {
	out := map[XdsType]bool{}

	// In case configTypes is not set, for example mesh configuration updated.
	// If push scoping is not enabled, we push all xds
	if len(pushEv.configsUpdated) == 0 {
		out[CDS] = true
		out[EDS] = true
		out[LDS] = true
		out[RDS] = true
		return out
	}

	// Note: CDS push must be followed by EDS, otherwise after Cluster is warmed, no ClusterLoadAssignment is retained.

	if proxy.Type == model.SidecarProxy {
		for config := range pushEv.configsUpdated {
			switch config {
			case collections.IstioNetworkingV1Alpha3Virtualservices.Resource().GroupVersionKind():
				out[LDS] = true
				out[RDS] = true
			case collections.IstioNetworkingV1Alpha3Gateways.Resource().GroupVersionKind():
				// Do not push
			case collections.IstioNetworkingV1Alpha3Serviceentries.Resource().GroupVersionKind():
				out[CDS] = true
				out[EDS] = true
				out[LDS] = true
				out[RDS] = true
			case collections.IstioNetworkingV1Alpha3Destinationrules.Resource().GroupVersionKind():
				out[CDS] = true
				out[EDS] = true
			case collections.IstioNetworkingV1Alpha3Envoyfilters.Resource().GroupVersionKind():
				out[CDS] = true
				out[EDS] = true
				out[LDS] = true
				out[RDS] = true
			case collections.IstioNetworkingV1Alpha3Sidecars.Resource().GroupVersionKind():
				out[CDS] = true
				out[EDS] = true
				out[LDS] = true
				out[RDS] = true
			case collections.IstioMixerV1ConfigClientQuotaspecs.Resource().GroupVersionKind(),
				collections.IstioMixerV1ConfigClientQuotaspecbindings.Resource().GroupVersionKind():
				// LDS must be pushed, otherwise RDS is not reloaded
				out[LDS] = true
				out[RDS] = true
			case collections.IstioAuthenticationV1Alpha1Policies.Resource().GroupVersionKind(),
				collections.IstioAuthenticationV1Alpha1Meshpolicies.Resource().GroupVersionKind():
				out[CDS] = true
				out[EDS] = true
				out[LDS] = true
			case collections.IstioRbacV1Alpha1Serviceroles.Resource().GroupVersionKind(),
				collections.IstioRbacV1Alpha1Servicerolebindings.Resource().GroupVersionKind(),
				collections.IstioRbacV1Alpha1Rbacconfigs.Resource().GroupVersionKind(),
				collections.IstioRbacV1Alpha1Clusterrbacconfigs.Resource().GroupVersionKind(),
				collections.IstioSecurityV1Beta1Authorizationpolicies.Resource().GroupVersionKind(),
				collections.IstioSecurityV1Beta1Requestauthentications.Resource().GroupVersionKind():
				out[LDS] = true
			case collections.IstioSecurityV1Beta1Peerauthentications.Resource().GroupVersionKind():
				out[CDS] = true
				out[EDS] = true
				out[LDS] = true
			default:
				out[CDS] = true
				out[EDS] = true
				out[LDS] = true
				out[RDS] = true
			}
			// To return asap
			if len(out) == 4 {
				return out
			}
		}
	} else {
		for config := range pushEv.configsUpdated {
			switch config {
			case collections.IstioNetworkingV1Alpha3Virtualservices.Resource().GroupVersionKind():
				out[LDS] = true
				out[RDS] = true
			case collections.IstioNetworkingV1Alpha3Gateways.Resource().GroupVersionKind():
				out[LDS] = true
				out[RDS] = true
			case collections.IstioNetworkingV1Alpha3Serviceentries.Resource().GroupVersionKind():
				out[CDS] = true
				out[EDS] = true
				out[LDS] = true
				out[RDS] = true
			case collections.IstioNetworkingV1Alpha3Destinationrules.Resource().GroupVersionKind():
				out[CDS] = true
				out[EDS] = true
			case collections.IstioNetworkingV1Alpha3Envoyfilters.Resource().GroupVersionKind():
				out[CDS] = true
				out[EDS] = true
				out[LDS] = true
				out[RDS] = true
			case collections.IstioNetworkingV1Alpha3Sidecars.Resource().GroupVersionKind(),
				collections.IstioMixerV1ConfigClientQuotaspecs.Resource().GroupVersionKind(),
				collections.IstioMixerV1ConfigClientQuotaspecbindings.Resource().GroupVersionKind():
				// do not push for gateway
			case collections.IstioAuthenticationV1Alpha1Policies.Resource().GroupVersionKind(),
				collections.IstioAuthenticationV1Alpha1Meshpolicies.Resource().GroupVersionKind():
				out[CDS] = true
				out[EDS] = true
				out[LDS] = true
			case collections.IstioRbacV1Alpha1Serviceroles.Resource().GroupVersionKind(),
				collections.IstioRbacV1Alpha1Servicerolebindings.Resource().GroupVersionKind(),
				collections.IstioRbacV1Alpha1Rbacconfigs.Resource().GroupVersionKind(),
				collections.IstioRbacV1Alpha1Clusterrbacconfigs.Resource().GroupVersionKind(),
				collections.IstioSecurityV1Beta1Authorizationpolicies.Resource().GroupVersionKind(),
				collections.IstioSecurityV1Beta1Requestauthentications.Resource().GroupVersionKind():
				out[LDS] = true
			case collections.IstioSecurityV1Beta1Peerauthentications.Resource().GroupVersionKind():
				out[CDS] = true
				out[EDS] = true
				out[LDS] = true
			default:
				out[CDS] = true
				out[EDS] = true
				out[LDS] = true
				out[RDS] = true
			}
			// To return asap
			if len(out) == 4 {
				return out
			}
		}
	}
	return out
}
