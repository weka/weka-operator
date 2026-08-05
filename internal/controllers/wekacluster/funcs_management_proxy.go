package wekacluster

import (
	"context"
	"fmt"
	"net"
	"strings"
	"text/template"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	k8sutil "github.com/weka/weka-k8s-api/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/pkg/util"
)

const (
	ManagementProxyName     = "management-proxy"
	ManagementConfigMapName = "management-proxy-config"
	// EnvoyContainersAnnotation is informational; updates are decided by the rendered config.
	EnvoyContainersAnnotation = "weka.io/proxy-containers"
	// EnvoyConfigHashAnnotation hashes the rendered config onto the pod template. Envoy never
	// reloads its config, so this is what rolls the pods when the ConfigMap changes.
	EnvoyConfigHashAnnotation = "weka.io/proxy-config-hash"

	// managementProxyAdminPort is Envoy's admin port: unauthenticated, exposes /quitquitquit.
	managementProxyAdminPort = 9901

	envoyHealthCheckInterval = 10 * time.Second
	envoyHealthCheckTimeout  = 5 * time.Second

	readinessInitialDelaySecondsDefault = 3

	// envoyHealthCheckRoundSeconds is the worst case before any upstream becomes selectable, since
	// every host starts unhealthy. Shared by readinessInitialDelaySeconds and minReadySeconds.
	envoyHealthCheckRoundSeconds = int32((envoyHealthCheckInterval + envoyHealthCheckTimeout) / time.Second)

	// Kept separate from ManagementProxyName despite the identical value: these go into the
	// Deployment's immutable Spec.Selector, so renaming the deployment must not change them.
	managementProxyAppLabel       = "weka-management-proxy"
	managementProxyComponentLabel = "management-proxy"
)

// managementProxySettings holds the chart's managementProxy values, resolved once per reconcile so
// the rendered config, pod spec and probes can't disagree.
type managementProxySettings struct {
	Replicas              int32
	HealthyPanicThreshold int32
	AdminBindAddress      string
	HostNetwork           bool

	// Parsed once from AdminBindAddress. An IPv6 wildcard also needs ipv4_compat on the admin
	// socket_address, or Envoy sets IPV6_V6ONLY and kubelet's IPv4 probe crash-loops the container.
	adminIsWildcard     bool
	adminIsIPv6Wildcard bool
}

func managementProxySettingsFromConfig() managementProxySettings {
	adminBindAddress := config.Config.ManagementProxyAdminBindAddress
	adminIP := net.ParseIP(adminBindAddress)
	adminIsWildcard := adminIP != nil && adminIP.IsUnspecified()

	return managementProxySettings{
		Replicas:              config.Config.ManagementProxyReplicas,
		HealthyPanicThreshold: config.Config.ManagementProxyHealthyPanicThreshold,
		AdminBindAddress:      adminBindAddress,
		HostNetwork:           config.Config.ManagementProxyHostNetwork,
		adminIsWildcard:       adminIsWildcard,
		adminIsIPv6Wildcard:   adminIsWildcard && adminIP.To4() == nil,
	}
}

// managementProxySelectorLabels returns the Deployment's Spec.Selector, also used as the base for
// the deployment and pod labels. Spec.Selector is immutable, so adding a key here breaks
// CreateOrUpdate on every existing proxy — pod-only labels belong on the pod template. A fresh map
// per call keeps the selector from sharing storage with a map a caller may grow.
func managementProxySelectorLabels(clusterName string) map[string]string {
	return map[string]string{
		"app":               managementProxyAppLabel,
		"weka.io/component": managementProxyComponentLabel,
		"weka.io/cluster":   clusterName,
	}
}

// EnsureManagementProxy creates or updates the Envoy proxy deployment and service
func (r *wekaClusterReconcilerLoop) EnsureManagementProxy(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	proxyName := r.getManagementProxyName()
	configMapName := r.getManagementConfigMapName()
	namespace := r.cluster.Namespace
	settings := managementProxySettingsFromConfig()

	// Get current active containers (up to 10) with base port
	activeContainers := r.selectActiveContainersForManagement()

	if len(activeContainers) == 0 {
		logger.Info("No active containers found for management proxy, skipping")
		return nil
	}

	// Ensure management proxy port is allocated (only when we have active containers)
	err := r.ensureManagementProxyPortAllocated(ctx)
	if err != nil {
		logger.Error(err, "Failed to allocate management proxy port")
		return err
	}

	// Rendered once so the ConfigMap and the Deployment's hash can't disagree. Rendered
	// unconditionally: a settings-only change leaves the backend container set untouched, so
	// pre-checking that set would never write it out.
	envoyConfig, err := r.generateEnvoyConfig(activeContainers, settings)
	if err != nil {
		logger.Error(err, "Failed to render management proxy Envoy config")
		return err
	}

	err = r.ensureManagementConfigMap(ctx, configMapName, namespace, activeContainers, envoyConfig)
	if err != nil {
		logger.Error(err, "Failed to create or update management proxy ConfigMap")
		return err
	}

	// Ensure the Deployment
	err = r.ensureManagementProxyDeployment(ctx, proxyName, configMapName, namespace, envoyConfig, settings)
	if err != nil {
		logger.Error(err, "Failed to create or update management proxy Deployment")
		return err
	}

	// Ensure the Service
	err = r.ensureManagementProxyService(ctx, proxyName, namespace)
	if err != nil {
		logger.Error(err, "Failed to create or update management proxy Service")
		return err
	}

	// Ensure the Ingress (if configured)
	err = r.ensureManagementProxyIngress(ctx, proxyName, namespace)
	if err != nil {
		logger.Error(err, "Failed to create or update management proxy Ingress")
		return err
	}

	logger.Info(fmt.Sprintf("Management proxy configured with %d backend endpoints", len(activeContainers)))

	return nil
}

// ensureManagementConfigMap creates or updates the Envoy ConfigMap. envoyConfig is rendered by the
// caller so the Deployment hashes the same bytes.
func (r *wekaClusterReconcilerLoop) ensureManagementConfigMap(ctx context.Context, configMapName, namespace string, activeContainers []*weka.WekaContainer, envoyConfig string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.getClient(), cm, func() error {
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		cm.Data["envoy.yaml"] = envoyConfig

		// Set annotations with container names
		if cm.Annotations == nil {
			cm.Annotations = make(map[string]string)
		}
		containerNames := make([]string, 0, len(activeContainers))
		for _, container := range activeContainers {
			containerNames = append(containerNames, container.Name)
		}
		cm.Annotations[EnvoyContainersAnnotation] = strings.Join(containerNames, ",")

		return controllerutil.SetControllerReference(r.cluster, cm, r.Manager.GetScheme())
	})

	return err
}

// ensureManagementProxyPortAllocated ensures management proxy port is allocated (backward compatibility)
func (r *wekaClusterReconcilerLoop) ensureManagementProxyPortAllocated(ctx context.Context) error {
	// If port already allocated, nothing to do
	if r.cluster.Status.Ports.ManagementProxyPort != 0 {
		return nil
	}

	// Get feature flags for port configuration
	featureFlags, err := r.GetFeatureFlags(ctx)
	if err != nil {
		return fmt.Errorf("failed to get feature flags: %w", err)
	}

	// Use the allocator interface which handles concurrency safely with optimistic locking
	resourcesAllocator := allocator.GetAllocator(r.getClient())

	// Allocate the port using the allocator interface
	err = resourcesAllocator.EnsureManagementProxyPort(ctx, r.cluster, featureFlags)
	if err != nil {
		return err
	}

	// Update cluster status with the allocated port
	return r.getClient().Status().Update(ctx, r.cluster)
}

// envoyConfigTemplateData is the substitution set for envoyConfigTemplate.
type envoyConfigTemplateData struct {
	ProxyPort             int
	HealthyPanicThreshold int32
	AdminAddress          string
	AdminPort             int
	// See adminIsIPv6Wildcard.
	AdminIPv4Compat bool
	// Duration literals ("10s").
	HealthCheckInterval string
	HealthCheckTimeout  string
	Endpoints           []envoyEndpoint
}

// envoyEndpoint is one entry in lb_endpoints. Address is quoted by the template so an IPv6 literal
// stays valid YAML.
type envoyEndpoint struct {
	Address string
	Port    int
}

// envoyConfigTemplate is the bootstrap config. A template rather than fmt.Sprintf: named fields
// keep an argument-order slip out of a YAML blob.
var envoyConfigTemplate = template.Must(template.New("envoy.yaml").Parse(`static_resources:
  listeners:
  - name: listener_0
    address:
      socket_address:
        # IPv4-only, not configurable: "::" would fail to bind on a node with IPv6 disabled, which
        # under hostNetwork means the proxy never starts.
        address: 0.0.0.0
        port_value: {{ .ProxyPort }}
    filter_chains:
    - filters:
      - name: envoy.filters.network.tcp_proxy
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy
          stat_prefix: weka_management
          cluster: weka_backend

  clusters:
  - name: weka_backend
    connect_timeout: 5s
    type: STATIC
    lb_policy: ROUND_ROBIN
    common_lb_config:
      # Always emitted: omitting it would mean Envoy's default 50, not the configured 0.
      healthy_panic_threshold:
        value: {{ .HealthyPanicThreshold }}
    load_assignment:
      cluster_name: weka_backend
      endpoints:
      - lb_endpoints:
{{- range .Endpoints }}
        - endpoint:
            address:
              socket_address:
                address: "{{ .Address }}"
                port_value: {{ .Port }}
{{- end }}
    # Health checks using HTTPS on /api/v2/healthcheck
    health_checks:
    - timeout: {{ .HealthCheckTimeout }}
      interval: {{ .HealthCheckInterval }}
      unhealthy_threshold: 2
      healthy_threshold: 1
      http_health_check:
        path: /api/v2/healthcheck
        host: localhost
      tls_options:
        alpn_protocols: ["h2","http/1.1"]
    # TLS context to skip certificate validation (no validation_context = accept any cert)
    transport_socket:
      name: envoy.transport_sockets.tls
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
        sni: localhost

admin:
  address:
    socket_address:
      address: "{{ .AdminAddress }}"
      port_value: {{ .AdminPort }}
{{- if .AdminIPv4Compat }}
      # Without this Envoy sets IPV6_V6ONLY, and kubelet's IPv4 probe fails.
      ipv4_compat: true
{{- end }}
`))

// generateEnvoyConfig renders the Envoy configuration YAML for the given backend containers.
func (r *wekaClusterReconcilerLoop) generateEnvoyConfig(activeContainers []*weka.WekaContainer, settings managementProxySettings) (string, error) {
	clusterBasePort := r.cluster.Status.Ports.BasePort
	managementProxyPort := r.cluster.Status.Ports.ManagementProxyPort

	// Build endpoints list
	endpoints := make([]envoyEndpoint, 0, len(activeContainers))
	for _, container := range activeContainers {
		managementIPs := container.Status.GetManagementIps()
		if len(managementIPs) == 0 {
			continue
		}
		// Parsed, not just checked for emptiness: an address Envoy rejects still changes the config
		// hash and would roll ready replicas into CrashLoopBackOff. Skip it, same as a missing IP.
		ip := managementIPs[0]
		if net.ParseIP(ip) == nil {
			continue
		}
		endpoints = append(endpoints, envoyEndpoint{Address: ip, Port: clusterBasePort})
	}

	// Envoy accepts an empty lb_endpoints, binds, then resets every connection -- and the TCP
	// readiness probe passes anyway, so the changed hash would roll working replicas into that
	// state. Refuse instead and leave the last good config in place. Reachable despite
	// IsContainerOperational's management-IP check, which is len()-only and passes on the [""] an
	// empty management_ips file yields.
	if len(endpoints) == 0 {
		return "", fmt.Errorf("none of the %d selected backend containers has a usable management IP; refusing to render an Envoy config with no endpoints", len(activeContainers))
	}

	data := envoyConfigTemplateData{
		ProxyPort:             managementProxyPort,
		HealthyPanicThreshold: settings.HealthyPanicThreshold,
		AdminAddress:          settings.AdminBindAddress,
		AdminPort:             managementProxyAdminPort,
		AdminIPv4Compat:       settings.adminIsIPv6Wildcard,
		HealthCheckInterval:   envoyHealthCheckInterval.String(),
		HealthCheckTimeout:    envoyHealthCheckTimeout.String(),
		Endpoints:             endpoints,
	}

	var rendered strings.Builder
	if err := envoyConfigTemplate.Execute(&rendered, data); err != nil {
		return "", fmt.Errorf("failed to render envoy config: %w", err)
	}

	return rendered.String(), nil
}

// adminProbeHost reports the host kubelet should dial to reach Envoy's admin endpoint, and whether
// it can reach it at all. An empty host means kubelet's default target, the pod IP.
//
// A wildcard bind answers on the pod IP ("::" covers IPv4 too via ipv4_compat). A loopback bind does
// not -- but under hostNetwork the pod runs in the node's network namespace, which is the one
// kubelet dials from, so kubelet's own loopback is Envoy's. Probing it keeps /ready without putting
// the unauthenticated admin API on the node network. That target is the node's, not the pod's, so it
// only distinguishes replicas because hostNetwork already admits one per node: a second would fail
// to bind 9901 rather than answer for the first.
//
// Unreachable only for a loopback bind on the pod network, which nothing defaults to.
func (s managementProxySettings) adminProbeHost() (string, bool) {
	if s.adminIsWildcard {
		return "", true
	}

	if s.HostNetwork {
		// Bare IP, validated at load time; kubelet brackets an IPv6 literal when it builds the URL.
		return s.AdminBindAddress, true
	}

	return "", false
}

// probeReflectsReady reports whether the probes below query /ready, as opposed to the TCP fallback
// that only proves the listener is bound.
func (s managementProxySettings) probeReflectsReady() bool {
	_, ok := s.adminProbeHost()
	return ok
}

// probeHandler returns the liveness/readiness handler for the Envoy container.
//
// /ready reflects Envoy's init state; the TCP fallback only proves the listener is bound, so it
// cannot catch an Envoy wedged with its listener up, and readiness can pass before any upstream is
// selectable (healthyPanicThreshold 0) -- readinessInitialDelaySeconds and minReadySeconds cover
// that second window. Reserved for the one configuration adminProbeHost can't reach.
func (s managementProxySettings) probeHandler(managementProxyPort int) corev1.ProbeHandler {
	if host, ok := s.adminProbeHost(); ok {
		return corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Host: host,
				Path: "/ready",
				Port: intstr.FromInt(managementProxyAdminPort),
			},
		}
	}

	return corev1.ProbeHandler{
		TCPSocket: &corev1.TCPSocketAction{
			Port: intstr.FromInt(managementProxyPort),
		},
	}
}

// readinessInitialDelaySeconds keeps a replica out of the Service until its first health-check
// round can resolve, so the TCP probe can't admit one that serves nothing (healthyPanicThreshold
// 0). Costs one round per rollout even at the default 50, where panic mode already covers the
// window. /ready needs no delay: Envoy withholds it until init completes.
func (s managementProxySettings) readinessInitialDelaySeconds() int32 {
	if s.probeReflectsReady() {
		return readinessInitialDelaySecondsDefault
	}

	return envoyHealthCheckRoundSeconds
}

// minReadySeconds holds the rollout back for one health-check round when the TCP probe is in use:
// under hostNetwork's MaxUnavailable 1/MaxSurge 0 the second replica could otherwise go down while
// the first is still mid round, leaving none able to serve. Zero when /ready gates readiness.
func (s managementProxySettings) minReadySeconds() int32 {
	if s.probeReflectsReady() {
		return 0
	}

	return envoyHealthCheckRoundSeconds
}

// updateStrategy picks the Deployment rollout strategy. Under hostNetwork a surge pod can land
// right back on the node whose ports the outgoing pod still holds — containerPort without hostPort
// is invisible to the scheduler — so replace in place instead and rely on the other replicas.
func (s managementProxySettings) updateStrategy() appsv1.DeploymentStrategy {
	if !s.HostNetwork {
		// Defaults spelled out rather than left nil: the API server fills them in on write, so a
		// nil would never match Get and CreateOrUpdate would Update every reconcile. Live clusters
		// only; the fake test client doesn't default.
		return appsv1.DeploymentStrategy{
			Type: appsv1.RollingUpdateDeploymentStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxUnavailable: ptr.To(intstr.FromString("25%")),
				MaxSurge:       ptr.To(intstr.FromString("25%")),
			},
		}
	}

	return appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxUnavailable: ptr.To(intstr.FromInt(1)),
			MaxSurge:       ptr.To(intstr.FromInt(0)),
		},
	}
}

// ensureManagementProxyDeployment creates or updates the Envoy Deployment. envoyConfig is hashed
// onto the pod template so a config change rolls the pods (see EnvoyConfigHashAnnotation).
func (r *wekaClusterReconcilerLoop) ensureManagementProxyDeployment(ctx context.Context, deploymentName, configMapName, namespace, envoyConfig string, settings managementProxySettings) error {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.getClient(), deployment, func() error {
		// Use allocated management proxy port
		managementProxyPort := r.cluster.Status.Ports.ManagementProxyPort

		// A separate map each: Spec.Selector is immutable, so it must not share storage with the
		// deployment or pod labels, which are free to grow.
		selectorLabels := managementProxySelectorLabels(r.cluster.Name)
		podLabels := managementProxySelectorLabels(r.cluster.Name)
		deployment.Labels = managementProxySelectorLabels(r.cluster.Name)

		deployment.Spec = appsv1.DeploymentSpec{
			Replicas:        ptr.To(settings.Replicas),
			Strategy:        settings.updateStrategy(),
			MinReadySeconds: settings.minReadySeconds(),
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
					Annotations: map[string]string{
						EnvoyConfigHashAnnotation: util.GetHash(envoyConfig, 8),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "envoy",
							Image: config.Config.EnvoyImage,
							Ports: []corev1.ContainerPort{
								{
									Name:          "weka-api",
									ContainerPort: int32(managementProxyPort),
									Protocol:      corev1.ProtocolTCP,
								},
								{
									Name:          "admin",
									ContainerPort: managementProxyAdminPort,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/etc/envoy",
									ReadOnly:  true,
								},
							},
							Command: []string{
								"envoy",
								"-c",
								"/etc/envoy/envoy.yaml",
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler:        settings.probeHandler(managementProxyPort),
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler:        settings.probeHandler(managementProxyPort),
								InitialDelaySeconds: settings.readinessInitialDelaySeconds(),
								PeriodSeconds:       5,
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: configMapName,
									},
								},
							},
						},
					},
					// Set hostNetwork based on configuration
					HostNetwork:  settings.HostNetwork,
					NodeSelector: r.cluster.Spec.NodeSelector,
					Tolerations:  k8sutil.ExpandTolerations([]corev1.Toleration{}, r.cluster.Spec.Tolerations, r.cluster.Spec.RawTolerations),
				},
			},
		}

		return controllerutil.SetControllerReference(r.cluster, deployment, r.Manager.GetScheme())
	})

	return err
}

// ensureManagementProxyService creates or updates the Service for the Envoy proxy
func (r *wekaClusterReconcilerLoop) ensureManagementProxyService(ctx context.Context, serviceName, namespace string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.getClient(), svc, func() error {
		// Set labels
		if svc.Labels == nil {
			svc.Labels = make(map[string]string)
		}
		svc.Labels["weka.io/cluster"] = r.cluster.Name
		svc.Labels["weka.io/service-type"] = "management"

		// Configure service type as ClusterIP with selector
		svc.Spec.Type = corev1.ServiceTypeClusterIP

		// Set selector to match Envoy pods
		svc.Spec.Selector = managementProxySelectorLabels(r.cluster.Name)

		// Use allocated management proxy port
		managementProxyPort := r.cluster.Status.Ports.ManagementProxyPort
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "weka-api",
				Port:       int32(managementProxyPort),
				TargetPort: intstr.FromString("weka-api"),
				Protocol:   corev1.ProtocolTCP,
			},
		}

		return controllerutil.SetControllerReference(r.cluster, svc, r.Manager.GetScheme())
	})

	return err
}

// getManagementProxyName returns the name of the management proxy deployment
func (r *wekaClusterReconcilerLoop) getManagementProxyName() string {
	return fmt.Sprintf("%s-%s", util.SanitizeK8sName(r.cluster.Name), ManagementProxyName)
}

// getManagementConfigMapName returns the name of the management proxy ConfigMap
func (r *wekaClusterReconcilerLoop) getManagementConfigMapName() string {
	return fmt.Sprintf("%s-%s", util.SanitizeK8sName(r.cluster.Name), ManagementConfigMapName)
}

// ensureManagementProxyIngress creates or updates the Ingress for the management proxy
func (r *wekaClusterReconcilerLoop) ensureManagementProxyIngress(ctx context.Context, serviceName, namespace string) error {
	// Check if ingress is configured (only baseDomain is required)
	if config.Config.ManagementProxyIngressBaseDomain == "" {
		// Ingress not configured, nothing to do
		return nil
	}

	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.getClient(), ingress, func() error {
		// Set labels
		if ingress.Labels == nil {
			ingress.Labels = make(map[string]string)
		}
		ingress.Labels["weka.io/cluster"] = r.cluster.Name
		ingress.Labels["weka.io/service-type"] = "management"

		// Generate hostname: namespace--clustername.basedomain
		hostname := fmt.Sprintf("%s--%s.%s", namespace, r.cluster.Name, config.Config.ManagementProxyIngressBaseDomain)

		// Use allocated management proxy port
		managementProxyPort := r.cluster.Status.Ports.ManagementProxyPort

		// Configure ingress spec
		pathTypePrefix := networkingv1.PathTypePrefix
		ingressSpec := networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: hostname,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathTypePrefix,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: serviceName,
											Port: networkingv1.ServiceBackendPort{
												Number: int32(managementProxyPort),
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		// Set ingress class if specified, otherwise use cluster default
		if config.Config.ManagementProxyIngressClass != "" {
			ingressSpec.IngressClassName = &config.Config.ManagementProxyIngressClass
		}

		ingress.Spec = ingressSpec

		return controllerutil.SetControllerReference(r.cluster, ingress, r.Manager.GetScheme())
	})

	return err
}
