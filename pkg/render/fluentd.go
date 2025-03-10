// Copyright (c) 2019-2022 Tigera, Inc. All rights reserved.

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

package render

import (
	"fmt"
	"strconv"

	v3 "github.com/tigera/api/pkg/apis/projectcalico/v3"
	"github.com/tigera/operator/pkg/render/common/networkpolicy"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorv1 "github.com/tigera/operator/api/v1"
	"github.com/tigera/operator/pkg/components"
	relasticsearch "github.com/tigera/operator/pkg/render/common/elasticsearch"
	rmeta "github.com/tigera/operator/pkg/render/common/meta"
	"github.com/tigera/operator/pkg/render/common/podsecuritypolicy"
	"github.com/tigera/operator/pkg/render/common/resourcequota"
	"github.com/tigera/operator/pkg/render/common/secret"
	"github.com/tigera/operator/pkg/tls/certificatemanagement"
	"github.com/tigera/operator/pkg/url"
)

const (
	LogCollectorNamespace                    = "tigera-fluentd"
	FluentdFilterConfigMapName               = "fluentd-filters"
	FluentdFilterFlowName                    = "flow"
	FluentdFilterDNSName                     = "dns"
	S3FluentdSecretName                      = "log-collector-s3-credentials"
	S3KeyIdName                              = "key-id"
	S3KeySecretName                          = "key-secret"
	FluentdPrometheusTLSSecretName           = "tigera-fluentd-prometheus-tls"
	FluentdMetricsService                    = "fluentd-metrics"
	FluentdMetricsPortName                   = "fluentd-metrics-port"
	FluentdMetricsPort                       = 9081
	FluentdPolicyName                        = networkpolicy.TigeraComponentPolicyPrefix + "allow-fluentd-node"
	filterHashAnnotation                     = "hash.operator.tigera.io/fluentd-filters"
	s3CredentialHashAnnotation               = "hash.operator.tigera.io/s3-credentials"
	splunkCredentialHashAnnotation           = "hash.operator.tigera.io/splunk-credentials"
	eksCloudwatchLogCredentialHashAnnotation = "hash.operator.tigera.io/eks-cloudwatch-log-credentials"
	fluentdDefaultFlush                      = "5s"
	ElasticsearchLogCollectorUserSecret      = "tigera-fluentd-elasticsearch-access"
	ElasticsearchEksLogForwarderUserSecret   = "tigera-eks-log-forwarder-elasticsearch-access"
	EksLogForwarderSecret                    = "tigera-eks-log-forwarder-secret"
	EksLogForwarderAwsId                     = "aws-id"
	EksLogForwarderAwsKey                    = "aws-key"
	SplunkFluentdTokenSecretName             = "logcollector-splunk-credentials"
	SplunkFluentdSecretTokenKey              = "token"
	SplunkFluentdCertificateSecretName       = "logcollector-splunk-public-certificate"
	SplunkFluentdSecretCertificateKey        = "ca.pem"
	SplunkFluentdSecretsVolName              = "splunk-certificates"
	SplunkFluentdDefaultCertDir              = "/etc/ssl/splunk/"
	SplunkFluentdDefaultCertPath             = SplunkFluentdDefaultCertDir + SplunkFluentdSecretCertificateKey

	probeTimeoutSeconds        int32 = 5
	probePeriodSeconds         int32 = 5
	probeWindowsTimeoutSeconds int32 = 10
	probeWindowsPeriodSeconds  int32 = 10

	// Default failure threshold for probes is 3. For the startupProbe tolerate more failures.
	probeFailureThreshold        int32 = 3
	startupProbeFailureThreshold int32 = 10

	fluentdName        = "tigera-fluentd"
	fluentdWindowsName = "tigera-fluentd-windows"

	FluentdNodeName        = "fluentd-node"
	fluentdNodeWindowsName = "fluentd-node-windows"

	eksLogForwarderName = "eks-log-forwarder"

	PacketCaptureAPIRole        = "packetcapture-api-role"
	PacketCaptureAPIRoleBinding = "packetcapture-api-role-binding"
)

var FluentdSourceEntityRule = v3.EntityRule{
	NamespaceSelector: fmt.Sprintf("name == '%s'", LogCollectorNamespace),
	Selector:          networkpolicy.KubernetesAppSelector(FluentdNodeName, fluentdNodeWindowsName),
}

var EKSLogForwarderEntityRule = networkpolicy.CreateSourceEntityRule(LogCollectorNamespace, eksLogForwarderName)

type FluentdFilters struct {
	Flow string
	DNS  string
}

type S3Credential struct {
	KeyId     []byte
	KeySecret []byte
}

type SplunkCredential struct {
	Token       []byte
	Certificate []byte
}

func Fluentd(cfg *FluentdConfiguration) Component {
	timeout := probeTimeoutSeconds
	period := probePeriodSeconds
	if cfg.OSType == rmeta.OSTypeWindows {
		timeout = probeWindowsTimeoutSeconds
		period = probeWindowsPeriodSeconds
	}

	return &fluentdComponent{
		cfg:          cfg,
		probeTimeout: timeout,
		probePeriod:  period,
	}
}

type EksCloudwatchLogConfig struct {
	AwsId         []byte
	AwsKey        []byte
	AwsRegion     string
	GroupName     string
	StreamPrefix  string
	FetchInterval int32
}

// FluentdConfiguration contains all the config information needed to render the component.
type FluentdConfiguration struct {
	LogCollector     *operatorv1.LogCollector
	ESSecrets        []*corev1.Secret
	ESClusterConfig  *relasticsearch.ClusterConfig
	S3Credential     *S3Credential
	SplkCredential   *SplunkCredential
	Filters          *FluentdFilters
	EKSConfig        *EksCloudwatchLogConfig
	PullSecrets      []*corev1.Secret
	Installation     *operatorv1.InstallationSpec
	ClusterDomain    string
	OSType           rmeta.OSType
	MetricsServerTLS certificatemanagement.KeyPairInterface
	TrustedBundle    certificatemanagement.TrustedBundle
	ManagedCluster   bool

	// Whether or not the cluster supports pod security policies.
	UsePSP bool
}

type fluentdComponent struct {
	cfg          *FluentdConfiguration
	image        string
	probeTimeout int32
	probePeriod  int32
}

func (c *fluentdComponent) ResolveImages(is *operatorv1.ImageSet) error {
	reg := c.cfg.Installation.Registry
	path := c.cfg.Installation.ImagePath
	prefix := c.cfg.Installation.ImagePrefix

	if c.cfg.OSType == rmeta.OSTypeWindows {
		var err error
		c.image, err = components.GetReference(components.ComponentFluentdWindows, reg, path, prefix, is)
		return err
	}

	var err error
	c.image, err = components.GetReference(components.ComponentFluentd, reg, path, prefix, is)
	if err != nil {
		return err
	}
	return err
}

func (c *fluentdComponent) SupportedOSType() rmeta.OSType {
	return c.cfg.OSType
}

func (c *fluentdComponent) fluentdName() string {
	if c.cfg.OSType == rmeta.OSTypeWindows {
		return fluentdWindowsName
	}
	return fluentdName
}

func (c *fluentdComponent) fluentdNodeName() string {
	if c.cfg.OSType == rmeta.OSTypeWindows {
		return fluentdNodeWindowsName
	}
	return FluentdNodeName
}

func (c *fluentdComponent) readinessCmd() []string {
	if c.cfg.OSType == rmeta.OSTypeWindows {
		// On Windows, we rely on bash via msys2 installed by the fluentd base image.
		return []string{`c:\ruby\msys64\usr\bin\bash.exe`, `-lc`, `/c/bin/readiness.sh`}
	}
	return []string{"sh", "-c", "/bin/readiness.sh"}
}

func (c *fluentdComponent) livenessCmd() []string {
	if c.cfg.OSType == rmeta.OSTypeWindows {
		// On Windows, we rely on bash via msys2 installed by the fluentd base image.
		return []string{`c:\ruby\msys64\usr\bin\bash.exe`, `-lc`, `/c/bin/liveness.sh`}
	}
	return []string{"sh", "-c", "/bin/liveness.sh"}
}

func (c *fluentdComponent) volumeHostPath() string {
	if c.cfg.OSType == rmeta.OSTypeWindows {
		return "c:/TigeraCalico"
	}
	return "/var/log/calico"
}

func (c *fluentdComponent) path(path string) string {
	if c.cfg.OSType == rmeta.OSTypeWindows {
		// Use c: path prefix for windows.
		return "c:" + path
	}
	// For linux just leave the path as-is.
	return path
}

func (c *fluentdComponent) Objects() ([]client.Object, []client.Object) {
	var objs, toDelete []client.Object
	objs = append(objs, CreateNamespace(LogCollectorNamespace, c.cfg.Installation.KubernetesProvider, PSSPrivileged))
	objs = append(objs, c.allowTigeraPolicy())
	objs = append(objs, secret.ToRuntimeObjects(secret.CopyToNamespace(LogCollectorNamespace, c.cfg.PullSecrets...)...)...)
	objs = append(objs, c.metricsService())

	if c.cfg.Installation.KubernetesProvider == operatorv1.ProviderGKE {
		// We do this only for GKE as other providers don't (yet?)
		// automatically add resource quota that constrains whether
		// components that are marked cluster or node critical
		// can be scheduled.
		objs = append(objs, c.fluentdResourceQuota())
	}
	if c.cfg.S3Credential != nil {
		objs = append(objs, c.s3CredentialSecret())
	}
	if c.cfg.SplkCredential != nil {
		objs = append(objs, secret.ToRuntimeObjects(secret.CopyToNamespace(LogCollectorNamespace, c.splunkCredentialSecret()...)...)...)
	}
	if c.cfg.Filters != nil {
		objs = append(objs, c.filtersConfigMap())
	}
	if c.cfg.EKSConfig != nil && c.cfg.OSType == rmeta.OSTypeLinux {
		if c.cfg.Installation.KubernetesProvider != operatorv1.ProviderOpenShift {
			objs = append(objs,
				c.eksLogForwarderClusterRole(),
				c.eksLogForwarderClusterRoleBinding())
			if c.cfg.UsePSP {
				objs = append(objs, c.eksLogForwarderPodSecurityPolicy())
			}
		}
		objs = append(objs, c.eksLogForwarderServiceAccount(),
			c.eksLogForwarderSecret(),
			c.eksLogForwarderDeployment())
	}

	// Windows PSP does not support allowedHostPaths yet.
	// See: https://github.com/kubernetes/kubernetes/issues/93165#issuecomment-693049808
	if c.cfg.Installation.KubernetesProvider != operatorv1.ProviderOpenShift && c.cfg.OSType == rmeta.OSTypeLinux {
		objs = append(objs,
			c.fluentdClusterRole(),
			c.fluentdClusterRoleBinding())
		if c.cfg.UsePSP {
			objs = append(objs, c.fluentdPodSecurityPolicy())
		}
	}

	objs = append(objs, secret.ToRuntimeObjects(secret.CopyToNamespace(LogCollectorNamespace, c.cfg.ESSecrets...)...)...)
	objs = append(objs, c.fluentdServiceAccount())
	objs = append(objs, c.packetCaptureApiRole(), c.packetCaptureApiRoleBinding())
	objs = append(objs, c.daemonset())

	return objs, toDelete
}

func (c *fluentdComponent) Ready() bool {
	return true
}

func (c *fluentdComponent) fluentdResourceQuota() *corev1.ResourceQuota {
	criticalPriorityClasses := []string{NodePriorityClassName}
	return resourcequota.ResourceQuotaForPriorityClassScope(resourcequota.TigeraCriticalResourceQuotaName, LogCollectorNamespace, criticalPriorityClasses)
}

func (c *fluentdComponent) s3CredentialSecret() *corev1.Secret {
	if c.cfg.S3Credential == nil {
		return nil
	}
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      S3FluentdSecretName,
			Namespace: LogCollectorNamespace,
		},
		Data: map[string][]byte{
			S3KeyIdName:     c.cfg.S3Credential.KeyId,
			S3KeySecretName: c.cfg.S3Credential.KeySecret,
		},
	}
}

func (c *fluentdComponent) filtersConfigMap() *corev1.ConfigMap {
	if c.cfg.Filters == nil {
		return nil
	}
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      FluentdFilterConfigMapName,
			Namespace: LogCollectorNamespace,
		},
		Data: map[string]string{
			FluentdFilterFlowName: c.cfg.Filters.Flow,
			FluentdFilterDNSName:  c.cfg.Filters.DNS,
		},
	}
}

func (c *fluentdComponent) splunkCredentialSecret() []*corev1.Secret {
	if c.cfg.SplkCredential == nil {
		return nil
	}
	var splunkSecrets []*corev1.Secret
	token := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      SplunkFluentdTokenSecretName,
			Namespace: LogCollectorNamespace,
		},
		Data: map[string][]byte{
			SplunkFluentdSecretTokenKey: c.cfg.SplkCredential.Token,
		},
	}

	splunkSecrets = append(splunkSecrets, token)

	if len(c.cfg.SplkCredential.Certificate) != 0 {
		certificate := &corev1.Secret{
			TypeMeta: metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      SplunkFluentdCertificateSecretName,
				Namespace: LogCollectorNamespace,
			},
			Data: map[string][]byte{
				SplunkFluentdSecretCertificateKey: c.cfg.SplkCredential.Certificate,
			},
		}
		splunkSecrets = append(splunkSecrets, certificate)
	}

	return splunkSecrets
}

func (c *fluentdComponent) fluentdServiceAccount() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{Kind: "ServiceAccount", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: c.fluentdNodeName(), Namespace: LogCollectorNamespace},
	}
}

// packetCaptureApiRole creates a role in the tigera-fluentd namespace to allow pod/exec
// only from fluentd pods. This is being used by the PacketCapture API and created
// by the operator after the namespace tigera-fluentd is created.
func (c *fluentdComponent) packetCaptureApiRole() *rbacv1.Role {
	return &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{Kind: "Role", APIVersion: "rbac.authorization.k8s.io/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      PacketCaptureAPIRole,
			Namespace: LogCollectorNamespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods/exec"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"list"},
			},
		},
	}
}

// packetCaptureApiRoleBinding creates a role binding within the tigera-fluentd namespace between the pod/exec role
// the service account tigera-manager. This is being used by the PacketCapture API and created
// by the operator after the namespace tigera-fluentd is created
func (c *fluentdComponent) packetCaptureApiRoleBinding() *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{Kind: "RoleBinding", APIVersion: "rbac.authorization.k8s.io/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      PacketCaptureAPIRoleBinding,
			Namespace: LogCollectorNamespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     PacketCaptureAPIRole,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      PacketCaptureServiceAccountName,
				Namespace: PacketCaptureNamespace,
			},
		},
	}
}

// managerDeployment creates a deployment for the Tigera Secure manager component.
func (c *fluentdComponent) daemonset() *appsv1.DaemonSet {
	var terminationGracePeriod int64 = 0
	maxUnavailable := intstr.FromInt(1)

	annots := c.cfg.TrustedBundle.HashAnnotations()

	if c.cfg.MetricsServerTLS != nil {
		annots[c.cfg.MetricsServerTLS.HashAnnotationKey()] = c.cfg.MetricsServerTLS.HashAnnotationValue()
	}
	if c.cfg.S3Credential != nil {
		annots[s3CredentialHashAnnotation] = rmeta.AnnotationHash(c.cfg.S3Credential)
	}
	if c.cfg.SplkCredential != nil {
		annots[splunkCredentialHashAnnotation] = rmeta.AnnotationHash(c.cfg.SplkCredential)
	}
	if c.cfg.Filters != nil {
		annots[filterHashAnnotation] = rmeta.AnnotationHash(c.cfg.Filters)
	}
	var initContainers []corev1.Container
	if c.cfg.MetricsServerTLS != nil && c.cfg.MetricsServerTLS.UseCertificateManagement() {
		initContainers = append(initContainers, c.cfg.MetricsServerTLS.InitContainer(LogCollectorNamespace))
	}

	podTemplate := relasticsearch.DecorateAnnotations(&corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: annots,
		},
		Spec: corev1.PodSpec{
			NodeSelector:                  map[string]string{},
			Tolerations:                   c.tolerations(),
			ImagePullSecrets:              secret.GetReferenceList(c.cfg.PullSecrets),
			TerminationGracePeriodSeconds: &terminationGracePeriod,
			InitContainers:                initContainers,
			Containers:                    []corev1.Container{c.container()},
			Volumes:                       c.volumes(),
			ServiceAccountName:            c.fluentdNodeName(),
		},
	}, c.cfg.ESClusterConfig, c.cfg.ESSecrets).(*corev1.PodTemplateSpec)

	ds := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{Kind: "DaemonSet", APIVersion: "apps/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.fluentdNodeName(),
			Namespace: LogCollectorNamespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Template: *podTemplate,
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: &maxUnavailable,
				},
			},
		},
	}

	setNodeCriticalPod(&(ds.Spec.Template))
	return ds
}

// logCollectorTolerations creates the node's tolerations.
func (c *fluentdComponent) tolerations() []corev1.Toleration {
	tolerations := []corev1.Toleration{
		{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
	}

	return tolerations
}

// container creates the fluentd container.
func (c *fluentdComponent) container() corev1.Container {
	// Determine environment to pass to the CNI init container.
	envs := c.envvars()
	volumeMounts := []corev1.VolumeMount{
		{MountPath: c.path("/var/log/calico"), Name: "var-log-calico"},
		{MountPath: c.path("/etc/fluentd/elastic"), Name: certificatemanagement.TrustedCertConfigMapName},
	}
	if c.cfg.Filters != nil {
		if c.cfg.Filters.Flow != "" {
			volumeMounts = append(volumeMounts,
				corev1.VolumeMount{
					Name:      "fluentd-filters",
					MountPath: c.path("/etc/fluentd/flow-filters.conf"),
					SubPath:   FluentdFilterFlowName,
				})
		}
		if c.cfg.Filters.DNS != "" {
			volumeMounts = append(volumeMounts,
				corev1.VolumeMount{
					Name:      "fluentd-filters",
					MountPath: c.path("/etc/fluentd/dns-filters.conf"),
					SubPath:   FluentdFilterDNSName,
				})
		}
	}

	if c.cfg.SplkCredential != nil && len(c.cfg.SplkCredential.Certificate) != 0 {
		volumeMounts = append(volumeMounts,
			corev1.VolumeMount{
				Name:      SplunkFluentdSecretsVolName,
				MountPath: c.path(SplunkFluentdDefaultCertDir),
			})
	}

	volumeMounts = append(volumeMounts, c.cfg.TrustedBundle.VolumeMount(c.SupportedOSType()))

	if c.cfg.MetricsServerTLS != nil {
		volumeMounts = append(volumeMounts, c.cfg.MetricsServerTLS.VolumeMount(c.SupportedOSType()))
	}

	isPrivileged := false
	// On OpenShift Fluentd needs privileged access to access logs on host path volume
	if c.cfg.Installation.KubernetesProvider == operatorv1.ProviderOpenShift {
		isPrivileged = true
	}

	return relasticsearch.ContainerDecorateENVVars(corev1.Container{
		Name:            "fluentd",
		Image:           c.image,
		Env:             envs,
		SecurityContext: &corev1.SecurityContext{Privileged: &isPrivileged},
		VolumeMounts:    volumeMounts,
		StartupProbe:    c.startup(),
		LivenessProbe:   c.liveness(),
		ReadinessProbe:  c.readiness(),
		Ports: []corev1.ContainerPort{{
			Name:          "metrics-port",
			ContainerPort: FluentdMetricsPort,
		}},
	}, c.cfg.ESClusterConfig.ClusterName(), ElasticsearchLogCollectorUserSecret, c.cfg.ClusterDomain, c.cfg.OSType)
}

func (c *fluentdComponent) metricsService() *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{Kind: "Service", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      FluentdMetricsService,
			Namespace: LogCollectorNamespace,
			Labels:    map[string]string{"k8s-app": FluentdNodeName},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"k8s-app": FluentdNodeName},
			Type:     corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Name:       FluentdMetricsPortName,
					Port:       int32(FluentdMetricsPort),
					TargetPort: intstr.FromInt(FluentdMetricsPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}

func (c *fluentdComponent) envvars() []corev1.EnvVar {
	envs := []corev1.EnvVar{
		{Name: "FLUENT_UID", Value: "0"},
		{Name: "FLOW_LOG_FILE", Value: c.path("/var/log/calico/flowlogs/flows.log")},
		{Name: "DNS_LOG_FILE", Value: c.path("/var/log/calico/dnslogs/dns.log")},
		{Name: "FLUENTD_ES_SECURE", Value: "true"},
		{Name: "NODENAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
	}

	if c.cfg.LogCollector.Spec.AdditionalStores != nil {
		s3 := c.cfg.LogCollector.Spec.AdditionalStores.S3
		if s3 != nil {
			envs = append(envs,
				corev1.EnvVar{
					Name: "AWS_KEY_ID",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: S3FluentdSecretName,
							},
							Key: S3KeyIdName,
						},
					},
				},
				corev1.EnvVar{
					Name: "AWS_SECRET_KEY",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: S3FluentdSecretName,
							},
							Key: S3KeySecretName,
						},
					},
				},
				corev1.EnvVar{Name: "S3_STORAGE", Value: "true"},
				corev1.EnvVar{Name: "S3_BUCKET_NAME", Value: s3.BucketName},
				corev1.EnvVar{Name: "AWS_REGION", Value: s3.Region},
				corev1.EnvVar{Name: "S3_BUCKET_PATH", Value: s3.BucketPath},
				corev1.EnvVar{Name: "S3_FLUSH_INTERVAL", Value: fluentdDefaultFlush},
			)
		}
		syslog := c.cfg.LogCollector.Spec.AdditionalStores.Syslog
		if syslog != nil {
			proto, host, port, _ := url.ParseEndpoint(syslog.Endpoint)
			envs = append(envs,
				corev1.EnvVar{Name: "SYSLOG_HOST", Value: host},
				corev1.EnvVar{Name: "SYSLOG_PORT", Value: port},
				corev1.EnvVar{Name: "SYSLOG_PROTOCOL", Value: proto},
				corev1.EnvVar{Name: "SYSLOG_FLUSH_INTERVAL", Value: fluentdDefaultFlush},
				corev1.EnvVar{
					Name: "SYSLOG_HOSTNAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "spec.nodeName",
						},
					},
				},
			)
			if syslog.PacketSize != nil {
				envs = append(envs,
					corev1.EnvVar{
						Name:  "SYSLOG_PACKET_SIZE",
						Value: fmt.Sprintf("%d", *syslog.PacketSize),
					},
				)
			}

			if syslog.LogTypes != nil {
				for _, t := range syslog.LogTypes {
					switch t {
					case operatorv1.SyslogLogAudit:
						envs = append(envs,
							corev1.EnvVar{Name: "SYSLOG_AUDIT_EE_LOG", Value: "true"},
						)
						envs = append(envs,
							corev1.EnvVar{Name: "SYSLOG_AUDIT_KUBE_LOG", Value: "true"},
						)
					case operatorv1.SyslogLogDNS:
						envs = append(envs,
							corev1.EnvVar{Name: "SYSLOG_DNS_LOG", Value: "true"},
						)
					case operatorv1.SyslogLogFlows:
						envs = append(envs,
							corev1.EnvVar{Name: "SYSLOG_FLOW_LOG", Value: "true"},
						)
					case operatorv1.SyslogLogIDSEvents:
						envs = append(envs,
							corev1.EnvVar{Name: "SYSLOG_IDS_EVENT_LOG", Value: "true"},
						)
					}
				}
			}
		}
		splunk := c.cfg.LogCollector.Spec.AdditionalStores.Splunk
		if splunk != nil {
			proto, host, port, _ := url.ParseEndpoint(splunk.Endpoint)
			envs = append(envs,
				corev1.EnvVar{
					Name: "SPLUNK_HEC_TOKEN",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: SplunkFluentdTokenSecretName,
							},
							Key: SplunkFluentdSecretTokenKey,
						},
					},
				},
				corev1.EnvVar{Name: "SPLUNK_FLOW_LOG", Value: "true"},
				corev1.EnvVar{Name: "SPLUNK_AUDIT_LOG", Value: "true"},
				corev1.EnvVar{Name: "SPLUNK_DNS_LOG", Value: "true"},
				corev1.EnvVar{Name: "SPLUNK_HEC_HOST", Value: host},
				corev1.EnvVar{Name: "SPLUNK_HEC_PORT", Value: port},
				corev1.EnvVar{Name: "SPLUNK_PROTOCOL", Value: proto},
				corev1.EnvVar{Name: "SPLUNK_FLUSH_INTERVAL", Value: fluentdDefaultFlush},
			)
			if len(c.cfg.SplkCredential.Certificate) != 0 {
				envs = append(envs,
					corev1.EnvVar{Name: "SPLUNK_CA_FILE", Value: SplunkFluentdDefaultCertPath},
				)
			}
		}
	}

	if c.cfg.Filters != nil {
		if c.cfg.Filters.Flow != "" {
			envs = append(envs,
				corev1.EnvVar{Name: "FLUENTD_FLOW_FILTERS", Value: "true"})
		}
		if c.cfg.Filters.DNS != "" {
			envs = append(envs,
				corev1.EnvVar{Name: "FLUENTD_DNS_FILTERS", Value: "true"})
		}
	}

	envs = append(envs,
		corev1.EnvVar{Name: "ELASTIC_FLOWS_INDEX_REPLICAS", Value: strconv.Itoa(c.cfg.ESClusterConfig.Replicas())},
		corev1.EnvVar{Name: "ELASTIC_DNS_INDEX_REPLICAS", Value: strconv.Itoa(c.cfg.ESClusterConfig.Replicas())},
		corev1.EnvVar{Name: "ELASTIC_AUDIT_INDEX_REPLICAS", Value: strconv.Itoa(c.cfg.ESClusterConfig.Replicas())},
		corev1.EnvVar{Name: "ELASTIC_BGP_INDEX_REPLICAS", Value: strconv.Itoa(c.cfg.ESClusterConfig.Replicas())},

		corev1.EnvVar{Name: "ELASTIC_FLOWS_INDEX_SHARDS", Value: strconv.Itoa(c.cfg.ESClusterConfig.FlowShards())},
		corev1.EnvVar{Name: "ELASTIC_DNS_INDEX_SHARDS", Value: strconv.Itoa(c.cfg.ESClusterConfig.Shards())},
		corev1.EnvVar{Name: "ELASTIC_AUDIT_INDEX_SHARDS", Value: strconv.Itoa(c.cfg.ESClusterConfig.Shards())},
		corev1.EnvVar{Name: "ELASTIC_BGP_INDEX_SHARDS", Value: strconv.Itoa(c.cfg.ESClusterConfig.Shards())},
	)

	if c.SupportedOSType() != rmeta.OSTypeWindows {
		envs = append(envs,
			corev1.EnvVar{Name: "CA_CRT_PATH", Value: c.cfg.TrustedBundle.MountPath()},
			corev1.EnvVar{Name: "TLS_KEY_PATH", Value: c.cfg.MetricsServerTLS.VolumeMountKeyFilePath()},
			corev1.EnvVar{Name: "TLS_CRT_PATH", Value: c.cfg.MetricsServerTLS.VolumeMountCertificateFilePath()},
		)
	}

	return envs
}

// The startup probe uses the same action as the liveness probe, but with
// a higher failure threshold and double the timeout to account for slow
// networks.
func (c *fluentdComponent) startup() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: c.livenessCmd(),
			},
		},
		TimeoutSeconds:   c.probeTimeout * 2,
		PeriodSeconds:    c.probePeriod * 2,
		FailureThreshold: startupProbeFailureThreshold,
	}
}

func (c *fluentdComponent) liveness() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: c.livenessCmd(),
			},
		},
		TimeoutSeconds:   c.probeTimeout,
		PeriodSeconds:    c.probePeriod,
		FailureThreshold: probeFailureThreshold,
	}
}

func (c *fluentdComponent) readiness() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: c.readinessCmd(),
			},
		},
		TimeoutSeconds:   c.probeTimeout,
		PeriodSeconds:    c.probePeriod,
		FailureThreshold: probeFailureThreshold,
	}
}

func (c *fluentdComponent) volumes() []corev1.Volume {
	dirOrCreate := corev1.HostPathDirectoryOrCreate

	volumes := []corev1.Volume{
		{
			Name: "var-log-calico",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: c.volumeHostPath(),
					Type: &dirOrCreate,
				},
			},
		},
	}
	if c.cfg.Filters != nil {
		volumes = append(volumes,
			corev1.Volume{
				Name: "fluentd-filters",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: FluentdFilterConfigMapName,
						},
					},
				},
			})
	}

	if c.cfg.SplkCredential != nil && len(c.cfg.SplkCredential.Certificate) != 0 {
		volumes = append(volumes,
			corev1.Volume{
				Name: SplunkFluentdSecretsVolName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: SplunkFluentdCertificateSecretName,
						Items: []corev1.KeyToPath{
							{Key: SplunkFluentdSecretCertificateKey, Path: SplunkFluentdSecretCertificateKey},
						},
					},
				},
			})
	}
	if c.cfg.MetricsServerTLS != nil {
		volumes = append(volumes, c.cfg.MetricsServerTLS.Volume())
	}
	volumes = append(volumes, trustedBundleVolume(c.cfg.TrustedBundle))

	return volumes
}

func (c *fluentdComponent) fluentdPodSecurityPolicy() *policyv1beta1.PodSecurityPolicy {
	psp := podsecuritypolicy.NewBasePolicy()
	psp.GetObjectMeta().SetName(c.fluentdName())
	psp.Spec.RequiredDropCapabilities = nil
	psp.Spec.AllowedCapabilities = []corev1.Capability{
		corev1.Capability("CAP_CHOWN"),
	}
	psp.Spec.Volumes = append(psp.Spec.Volumes, policyv1beta1.HostPath)
	psp.Spec.AllowedHostPaths = []policyv1beta1.AllowedHostPath{
		{
			PathPrefix: c.path("/var/log/calico"),
			ReadOnly:   false,
		},
	}
	psp.Spec.RunAsUser.Rule = policyv1beta1.RunAsUserStrategyRunAsAny
	return psp
}

func (c *fluentdComponent) fluentdClusterRoleBinding() *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{Kind: "ClusterRoleBinding", APIVersion: "rbac.authorization.k8s.io/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: c.fluentdName(),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     c.fluentdName(),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      c.fluentdNodeName(),
				Namespace: LogCollectorNamespace,
			},
		},
	}
}

func (c *fluentdComponent) fluentdClusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{Kind: "ClusterRole", APIVersion: "rbac.authorization.k8s.io/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: c.fluentdName(),
		},

		Rules: []rbacv1.PolicyRule{
			{
				// Allow access to the pod security policy in case this is enforced on the cluster
				APIGroups:     []string{"policy"},
				Resources:     []string{"podsecuritypolicies"},
				Verbs:         []string{"use"},
				ResourceNames: []string{c.fluentdName()},
			},
		},
	}
}

func (c *fluentdComponent) eksLogForwarderServiceAccount() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{Kind: "ServiceAccount", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: eksLogForwarderName, Namespace: LogCollectorNamespace},
	}
}

func (c *fluentdComponent) eksLogForwarderSecret() *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      EksLogForwarderSecret,
			Namespace: LogCollectorNamespace,
		},
		Data: map[string][]byte{
			EksLogForwarderAwsId:  c.cfg.EKSConfig.AwsId,
			EksLogForwarderAwsKey: c.cfg.EKSConfig.AwsKey,
		},
	}
}

func (c *fluentdComponent) eksLogForwarderDeployment() *appsv1.Deployment {
	annots := map[string]string{
		eksCloudwatchLogCredentialHashAnnotation: rmeta.AnnotationHash(c.cfg.EKSConfig),
	}

	envVars := []corev1.EnvVar{
		// Meta flags.
		{Name: "LOG_LEVEL", Value: "info"},
		{Name: "FLUENT_UID", Value: "0"},
		// Use fluentd for EKS log forwarder.
		{Name: "MANAGED_K8S", Value: "true"},
		{Name: "K8S_PLATFORM", Value: "eks"},
		{Name: "FLUENTD_ES_SECURE", Value: "true"},
		// Cloudwatch config, credentials.
		{Name: "EKS_CLOUDWATCH_LOG_GROUP", Value: c.cfg.EKSConfig.GroupName},
		{Name: "EKS_CLOUDWATCH_LOG_STREAM_PREFIX", Value: c.cfg.EKSConfig.StreamPrefix},
		{Name: "EKS_CLOUDWATCH_LOG_FETCH_INTERVAL", Value: fmt.Sprintf("%d", c.cfg.EKSConfig.FetchInterval)},
		{Name: "AWS_REGION", Value: c.cfg.EKSConfig.AwsRegion},
		{Name: "AWS_ACCESS_KEY_ID", ValueFrom: secret.GetEnvVarSource(EksLogForwarderSecret, EksLogForwarderAwsId, false)},
		{Name: "AWS_SECRET_ACCESS_KEY", ValueFrom: secret.GetEnvVarSource(EksLogForwarderSecret, EksLogForwarderAwsKey, false)},
	}

	var eksLogForwarderReplicas int32 = 1

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      eksLogForwarderName,
			Namespace: LogCollectorNamespace,
			Labels: map[string]string{
				"k8s-app": eksLogForwarderName,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &eksLogForwarderReplicas,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"k8s-app": eksLogForwarderName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      eksLogForwarderName,
					Namespace: LogCollectorNamespace,
					Labels: map[string]string{
						"k8s-app": eksLogForwarderName,
					},
					Annotations: annots,
				},
				Spec: corev1.PodSpec{
					Tolerations:        c.cfg.Installation.ControlPlaneTolerations,
					ServiceAccountName: eksLogForwarderName,
					ImagePullSecrets:   secret.GetReferenceList(c.cfg.PullSecrets),
					InitContainers: []corev1.Container{relasticsearch.ContainerDecorateENVVars(corev1.Container{
						Name:         eksLogForwarderName + "-startup",
						Image:        c.image,
						Command:      []string{c.path("/bin/eks-log-forwarder-startup")},
						Env:          envVars,
						VolumeMounts: c.eksLogForwarderVolumeMounts(),
					}, c.cfg.ESClusterConfig.ClusterName(), ElasticsearchEksLogForwarderUserSecret, c.cfg.ClusterDomain, c.cfg.OSType)},
					Containers: []corev1.Container{relasticsearch.ContainerDecorateENVVars(corev1.Container{
						Name:         eksLogForwarderName,
						Image:        c.image,
						Env:          envVars,
						VolumeMounts: c.eksLogForwarderVolumeMounts(),
					}, c.cfg.ESClusterConfig.ClusterName(), ElasticsearchEksLogForwarderUserSecret, c.cfg.ClusterDomain, c.cfg.OSType)},
					Volumes: c.eksLogForwarderVolumes(),
				},
			},
		},
	}
}

func trustedBundleVolume(bundle certificatemanagement.TrustedBundle) corev1.Volume {
	volume := bundle.Volume()
	// We mount the bundle under two names; the standard name and the name for the expected elastic cert.
	volume.ConfigMap.Items = []corev1.KeyToPath{
		{Key: certificatemanagement.TrustedCertConfigMapKeyName, Path: certificatemanagement.TrustedCertConfigMapKeyName},
		{Key: certificatemanagement.TrustedCertConfigMapKeyName, Path: SplunkFluentdSecretCertificateKey},
	}
	return volume
}

func (c *fluentdComponent) eksLogForwarderVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		relasticsearch.DefaultVolumeMount(c.cfg.OSType),
		{
			Name:      "plugin-statefile-dir",
			MountPath: c.path("/fluentd/cloudwatch-logs/"),
		},
		{
			Name:      certificatemanagement.TrustedCertConfigMapName,
			MountPath: c.path("/etc/fluentd/elastic/"),
		},
	}
}

func (c *fluentdComponent) eksLogForwarderVolumes() []corev1.Volume {
	return []corev1.Volume{
		trustedBundleVolume(c.cfg.TrustedBundle),
		{
			Name: "plugin-statefile-dir",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: nil,
			},
		},
	}
}

func (c *fluentdComponent) eksLogForwarderPodSecurityPolicy() *policyv1beta1.PodSecurityPolicy {
	psp := podsecuritypolicy.NewBasePolicy()
	psp.GetObjectMeta().SetName(eksLogForwarderName)
	psp.Spec.RunAsUser.Rule = policyv1beta1.RunAsUserStrategyRunAsAny
	return psp
}

func (c *fluentdComponent) eksLogForwarderClusterRoleBinding() *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{Kind: "ClusterRoleBinding", APIVersion: "rbac.authorization.k8s.io/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: eksLogForwarderName,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     eksLogForwarderName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      eksLogForwarderName,
				Namespace: LogCollectorNamespace,
			},
		},
	}
}

func (c *fluentdComponent) eksLogForwarderClusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{Kind: "ClusterRole", APIVersion: "rbac.authorization.k8s.io/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: eksLogForwarderName,
		},

		Rules: []rbacv1.PolicyRule{
			{
				// Allow access to the pod security policy in case this is enforced on the cluster
				APIGroups:     []string{"policy"},
				Resources:     []string{"podsecuritypolicies"},
				Verbs:         []string{"use"},
				ResourceNames: []string{eksLogForwarderName},
			},
		},
	}
}

func (c *fluentdComponent) allowTigeraPolicy() *v3.NetworkPolicy {
	egressRules := []v3.Rule{}
	if c.cfg.ManagedCluster {
		egressRules = append(egressRules, v3.Rule{
			Action:   v3.Deny,
			Protocol: &networkpolicy.TCPProtocol,
			Source:   v3.EntityRule{},
			Destination: v3.EntityRule{
				NamespaceSelector: fmt.Sprintf("projectcalico.org/name == '%s'", GuardianNamespace),
				Selector:          networkpolicy.KubernetesAppSelector(GuardianServiceName),
				NotPorts:          networkpolicy.Ports(8080),
			},
		})
	} else {
		egressRules = append(egressRules, v3.Rule{
			Action:   v3.Deny,
			Protocol: &networkpolicy.TCPProtocol,
			Source:   v3.EntityRule{},
			Destination: v3.EntityRule{
				NamespaceSelector: fmt.Sprintf("projectcalico.org/name == '%s'", ElasticsearchNamespace),
				Selector:          networkpolicy.KubernetesAppSelector("tigera-secure-es-gateway"),
				NotPorts:          networkpolicy.Ports(5554),
			},
		})
		egressRules = networkpolicy.AppendDNSEgressRules(egressRules, c.cfg.Installation.KubernetesProvider == operatorv1.ProviderOpenShift)
	}
	egressRules = append(egressRules, v3.Rule{
		Action: v3.Allow,
	})

	return &v3.NetworkPolicy{
		TypeMeta: metav1.TypeMeta{Kind: "NetworkPolicy", APIVersion: "projectcalico.org/v3"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      FluentdPolicyName,
			Namespace: LogCollectorNamespace,
		},
		Spec: v3.NetworkPolicySpec{
			Order:                  &networkpolicy.HighPrecedenceOrder,
			Tier:                   networkpolicy.TigeraComponentTierName,
			Selector:               networkpolicy.KubernetesAppSelector(FluentdNodeName, fluentdNodeWindowsName),
			ServiceAccountSelector: "",
			Types:                  []v3.PolicyType{v3.PolicyTypeIngress, v3.PolicyTypeEgress},
			Ingress: []v3.Rule{
				{
					Action:   v3.Allow,
					Protocol: &networkpolicy.TCPProtocol,
					Source:   networkpolicy.PrometheusSourceEntityRule,
					Destination: v3.EntityRule{
						Ports: networkpolicy.Ports(FluentdMetricsPort),
					},
				},
			},
			Egress: egressRules,
		},
	}
}
