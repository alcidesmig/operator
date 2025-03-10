// Copyright (c) 2020-2022 Tigera, Inc. All rights reserved.

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

package utils

import (
	"context"
	"fmt"
	"strings"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	operatorv1 "github.com/tigera/operator/api/v1"
)

var log = logf.Log.WithName("discovery")

// RequiresTigeraSecure determines if the configuration requires we start the tigera secure
// controllers.
func RequiresTigeraSecure(cfg *rest.Config) (bool, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return false, err
	}

	// Use the discovery client to determine if the tigera secure specific APIs exist.
	resources, err := clientset.Discovery().ServerResourcesForGroupVersion("operator.tigera.io/v1")
	if err != nil {
		return false, err
	}
	for _, r := range resources.APIResources {
		switch r.Kind {
		case "LogCollector":
			fallthrough
		case "LogStorage":
			fallthrough
		case "AmazonCloudIntegration":
			fallthrough
		case "Compliance":
			fallthrough
		case "IntrusionDetection":
			fallthrough
		case "ApplicationLayer":
			fallthrough
		case "Monitor":
			fallthrough
		case "ManagementCluster":
			fallthrough
		case "ManagementClusterConnection":
			return true, nil
		}
	}
	return false, nil
}

// RequiresAmazonController determines if the configuration requires we start the aws
// controllers.
func RequiresAmazonController(cfg *rest.Config) (bool, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return false, err
	}

	// Use the discovery client to determine if the amazoncloudintegration APIs exist.
	resources, err := clientset.Discovery().ServerResourcesForGroupVersion("operator.tigera.io/v1")
	if err != nil {
		return false, err
	}
	for _, r := range resources.APIResources {
		if r.Kind == "AmazonCloudIntegration" {
			return true, nil
		}
	}
	return false, nil
}

func AutoDiscoverProvider(ctx context.Context, clientset kubernetes.Interface) (operatorv1.Provider, error) {

	// List of detected providers, for detecting conflicts.
	detectedProviders := []operatorv1.Provider{}

	// First, try to determine the platform based on the present API groups.
	if platform, err := autodetectFromGroup(clientset); err != nil {
		return operatorv1.ProviderNone, fmt.Errorf("Failed to check provider based on API groups: %s", err)
	} else if platform != nil {
		// We detected platform(s). Append it to detected ones.
		detectedProviders = append(detectedProviders, platform...)
	}

	// We failed to determine the platform based on API groups. Some platforms can be detected in other ways, though.
	if dockeree, err := isDockerEE(ctx, clientset); err != nil {
		return operatorv1.ProviderNone, fmt.Errorf("Failed to check if Docker EE is the provider: %s", err)
	} else if dockeree {
		detectedProviders = append(detectedProviders, operatorv1.ProviderDockerEE)
	}

	// We failed to determine the platform based on API groups. Some platforms can be detected in other ways, though.
	if eks, err := isEKS(ctx, clientset); err != nil {
		return operatorv1.ProviderNone, fmt.Errorf("Failed to check if EKS is the provider: %s", err)
	} else if eks {
		detectedProviders = append(detectedProviders, operatorv1.ProviderEKS)

	}

	// Attempt to detect RKE Version 2, which also cannot be done via API groups.
	if rke2, err := isRKE2(ctx, clientset); err != nil {
		return operatorv1.ProviderNone, fmt.Errorf("Failed to check if RKE2 is the provider: %s", err)
	} else if rke2 {
		detectedProviders = append(detectedProviders, operatorv1.ProviderRKE2)
	}

	// More than one provider was detected. Can't be sure which one is correct.
	if len(detectedProviders) > 1 {
		return operatorv1.ProviderNone, fmt.Errorf(
			"Failed to assert provider caused by detection of more than one. Detected providers: %s",
			detectedProviders)
	}
	// Only one provider was detected.
	if len(detectedProviders) == 1 {
		return detectedProviders[0], nil
	}

	// Couldn't detect any specific platform.
	return operatorv1.ProviderNone, nil
}

// autodetectFromGroup auto detects the platform based on the API groups that are present.
func autodetectFromGroup(c kubernetes.Interface) ([]operatorv1.Provider, error) {
	// List of detected providers, for detecting conflicts.
	detectedProviders := []operatorv1.Provider{}

	groups, err := c.Discovery().ServerGroups()
	if err != nil {
		return nil, err
	}
	for _, g := range groups.Groups {
		if g.Name == "config.openshift.io" {
			// Running on OpenShift.
			detectedProviders = append(detectedProviders, operatorv1.ProviderOpenShift)
		}

		if g.Name == "networking.gke.io" {
			// Running on GKE.
			detectedProviders = append(detectedProviders, operatorv1.ProviderGKE)
		}
	}
	return detectedProviders, nil
}

// isDockerEE returns true if running on a Docker Enterprise cluster, and false otherwise.
// Docker EE doesn't have any provider-specific API groups, so we need to use a different approach than
// we use for other platforms in autodetectFromGroup.
func isDockerEE(ctx context.Context, c kubernetes.Interface) (bool, error) {
	masterNodes, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/master"})
	if err != nil {
		return false, err
	}
	for _, n := range masterNodes.Items {
		for l := range n.Labels {
			if strings.HasPrefix(l, "com.docker.ucp") {
				return true, nil
			}
		}
	}
	return false, nil
}

// isEKS returns true if running on an EKS cluster, and false otherwise.
// EKS doesn't have any provider-specific API groups, so we need to use a different approach than
// we use for other platforms in autodetectFromGroup.
func isEKS(ctx context.Context, c kubernetes.Interface) (bool, error) {
	cm, err := c.CoreV1().ConfigMaps("kube-system").Get(ctx, "eks-certificates-controller", metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return (cm != nil), nil
}

// isRKE2 returns true if running on an RKE2 cluster, and false otherwise.
// While the presence of Rancher can be determined based on API Groups, it's important to
// differentiate between versions, which requires another approach. In this case,
// the presence of an "rke2" configmap in kube-system namespace is used.
func isRKE2(ctx context.Context, c kubernetes.Interface) (bool, error) {
	cm, err := c.CoreV1().ConfigMaps("kube-system").Get(ctx, "rke2", metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return (cm != nil), nil
}

// SupportsPodSecurityPolicies returns true if the cluster contains the policy/v1beta1 PodSecurityPolicy API,
// and false otherwise. This API is scheuled to be removed in Kubernetes v1.25, but should still be used
// in earlier Kubernetes versions.
func SupportsPodSecurityPolicies(c kubernetes.Interface) (bool, error) {
	resources, err := c.Discovery().ServerResourcesForGroupVersion("policy/v1beta1")
	if err != nil {
		if kerrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	for _, r := range resources.APIResources {
		if r.Kind == "PodSecurityPolicy" {
			return true, nil
		}
	}
	return false, nil
}
