package wekacluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/services/discovery"
)

const (
	MaxManagementProxyEndpoints = 10
	ManagementProxyName         = "management-proxy"
	ManagementConfigMapName     = "management-proxy-config"
	EnvoyImage                  = "envoyproxy/envoy:v1.31-latest"
	EnvoyContainersAnnotation   = "weka.io/proxy-containers"
)

// EnsureManagementProxy creates or updates the Envoy proxy deployment and service
func (r *wekaClusterReconcilerLoop) EnsureManagementProxy(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	proxyName := r.getManagementProxyName()
	configMapName := r.getManagementConfigMapName()
	namespace := r.cluster.Namespace

	// Get current active containers (up to 10) with base port
	activeContainers := r.selectActiveContainersForManagement()

	if len(activeContainers) == 0 {
		logger.Info("No active containers found for management proxy, skipping")
		return nil
	}

	// Check if we need to update the ConfigMap
	existingConfigMap := &corev1.ConfigMap{}
	err := r.getClient().Get(ctx, client.ObjectKey{Name: configMapName, Namespace: namespace}, existingConfigMap)
	needsUpdate := true
	if err == nil {
		// ConfigMap exists, check if containers changed
		needsUpdate = r.shouldUpdateProxyConfig(existingConfigMap, activeContainers)
	}

	if needsUpdate {
		// Create or update the ConfigMap
		err = r.ensureManagementConfigMap(ctx, configMapName, namespace, activeContainers)
		if err != nil {
			logger.Error(err, "Failed to create or update management proxy ConfigMap")
			return err
		}
	}

	// Ensure the Deployment
	err = r.ensureManagementProxyDeployment(ctx, proxyName, configMapName, namespace)
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

	logger.Info(fmt.Sprintf("Management proxy configured with %d backend endpoints", len(activeContainers)))

	return nil
}

// ensureManagementConfigMap creates or updates the Envoy ConfigMap
func (r *wekaClusterReconcilerLoop) ensureManagementConfigMap(ctx context.Context, configMapName, namespace string, activeContainers []*weka.WekaContainer) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.getClient(), cm, func() error {
		// Generate Envoy configuration
		envoyConfig := r.generateEnvoyConfig(activeContainers)

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

// generateEnvoyConfig generates the Envoy configuration YAML
func (r *wekaClusterReconcilerLoop) generateEnvoyConfig(activeContainers []*weka.WekaContainer) string {
	clusterBasePort := r.cluster.Status.Ports.BasePort
	if clusterBasePort == 0 {
		clusterBasePort = 14000
	}

	// Build endpoints list
	endpoints := []string{}
	for _, container := range activeContainers {
		managementIPs := container.Status.GetManagementIps()
		if len(managementIPs) == 0 {
			continue
		}
		// Use first management IP
		ip := managementIPs[0]
		endpoints = append(endpoints, fmt.Sprintf(`        - endpoint:
            address:
              socket_address:
                address: %s
                port_value: %d`, ip, clusterBasePort))
	}

	config := fmt.Sprintf(`static_resources:
  listeners:
  - name: listener_0
    address:
      socket_address:
        address: 0.0.0.0
        port_value: %d
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
    load_assignment:
      cluster_name: weka_backend
      endpoints:
      - lb_endpoints:
%s
    # Health checks using HTTPS on /api/v2/healthcheck
    health_checks:
    - timeout: 5s
      interval: 10s
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
      address: 0.0.0.0
      port_value: 9901
`, clusterBasePort, strings.Join(endpoints, "\n"))

	return config
}

// ensureManagementProxyDeployment creates or updates the Envoy Deployment
func (r *wekaClusterReconcilerLoop) ensureManagementProxyDeployment(ctx context.Context, deploymentName, configMapName, namespace string) error {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.getClient(), deployment, func() error {
		replicas := int32(2)

		// Labels
		labels := map[string]string{
			"app":                "weka-management-proxy",
			"weka.io/cluster":    r.cluster.Name,
			"weka.io/component":  "management-proxy",
		}

		deployment.Labels = labels

		deployment.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":               "weka-management-proxy",
					"weka.io/cluster":   r.cluster.Name,
					"weka.io/component": "management-proxy",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":               "weka-management-proxy",
						"weka.io/cluster":   r.cluster.Name,
						"weka.io/component": "management-proxy",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "envoy",
							Image: EnvoyImage,
							Ports: []corev1.ContainerPort{
								{
									Name:          "weka-api",
									ContainerPort: int32(r.cluster.Status.Ports.BasePort),
									Protocol:      corev1.ProtocolTCP,
								},
								{
									Name:          "admin",
									ContainerPort: 9901,
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
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/ready",
										Port: intstr.FromInt(9901),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/ready",
										Port: intstr.FromInt(9901),
									},
								},
								InitialDelaySeconds: 3,
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
		svc.Spec.Selector = map[string]string{
			"app":               "weka-management-proxy",
			"weka.io/cluster":   r.cluster.Name,
			"weka.io/component": "management-proxy",
		}

		// Define ports
		clusterBasePort := r.cluster.Status.Ports.BasePort
		if clusterBasePort == 0 {
			clusterBasePort = 14000
		}
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "weka-api",
				Port:       int32(clusterBasePort),
				TargetPort: intstr.FromString("weka-api"),
				Protocol:   corev1.ProtocolTCP,
			},
		}

		return controllerutil.SetControllerReference(r.cluster, svc, r.Manager.GetScheme())
	})

	return err
}

// shouldUpdateProxyConfig checks if we should update the proxy config
func (r *wekaClusterReconcilerLoop) shouldUpdateProxyConfig(existingConfigMap *corev1.ConfigMap, newActiveContainers []*weka.WekaContainer) bool {
	// Get container names from existing ConfigMap annotation
	containerAnnotation, exists := existingConfigMap.Annotations[EnvoyContainersAnnotation]
	if !exists {
		return true
	}

	existingContainerNames := strings.Split(containerAnnotation, ",")

	// If we already have 10 endpoints
	if len(existingContainerNames) >= MaxManagementProxyEndpoints {
		// Validate all current containers are still active
		allStillActive := true
		for _, name := range existingContainerNames {
			containerStillActive := false
			for _, container := range r.containers {
				if container.Name == name && discovery.IsContainerOperational(container) {
					// Also check it matches base port
					if container.GetPort() == r.cluster.Status.Ports.BasePort {
						containerStillActive = true
						break
					}
				}
			}
			if !containerStillActive {
				allStillActive = false
				break
			}
		}

		// If all current containers are still active, skip update
		if allStillActive {
			return false
		}
	}

	// If we have fewer than 10 and there are more healthy containers available, we should update
	if len(existingContainerNames) < MaxManagementProxyEndpoints && len(newActiveContainers) > len(existingContainerNames) {
		return true
	}

	// If container list changed, update
	newContainerNames := make(map[string]bool)
	for _, container := range newActiveContainers {
		newContainerNames[container.Name] = true
	}

	for _, name := range existingContainerNames {
		if !newContainerNames[name] {
			return true
		}
	}

	return false
}

// getManagementProxyName returns the name of the management proxy deployment
func (r *wekaClusterReconcilerLoop) getManagementProxyName() string {
	return fmt.Sprintf("%s-%s", r.cluster.Name, ManagementProxyName)
}

// getManagementConfigMapName returns the name of the management proxy ConfigMap
func (r *wekaClusterReconcilerLoop) getManagementConfigMapName() string {
	return fmt.Sprintf("%s-%s", r.cluster.Name, ManagementConfigMapName)
}
