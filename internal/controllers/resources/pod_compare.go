package resources

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

const (
	otelInstallerContainerName = "otel-packages-installer"
	resourceHugepages2Mi       = corev1.ResourceName(corev1.ResourceHugePagesPrefix + "2Mi")
)

// comparedEnvNames are the weka container env names DiffPods compares, both directions.
var comparedEnvNames = map[string]struct{}{
	"CORES": {}, "MEMORY": {}, "DPDK_BASE_MEMORY_MB": {}, "MAX_TRACE_CAPACITY_GB": {}, "ENSURE_FREE_SPACE_GB": {},
	"DUMPER_CONFIG_MODE": {}, "CPU_POLICY": {}, "CORE_IDS": {}, "NON_DATAPATH_CORE_IDS": {}, "IS_IPV6": {},
	"NETWORK_DEVICE": {}, "SUBNETS": {}, "NETWORK_SELECTORS": {}, "MANAGEMENT_IPS_SELECTORS": {},
	"BIND_MANAGEMENT_ALL": {}, "NET_GATEWAY": {}, "NET_NETMASK": {}, "UDP_MODE": {}, "NVIDIA_VF_SINGLE_IP": {},
	"NAME": {}, "MODE": {}, "WEKA_CONTAINER_ID": {}, "MACHINE_IDENTIFIER": {}, "NO_RESERVE_SPACE": {},
	"WEKA_OPERATOR_DEBUG_SLEEP": {}, "WEKA_PERSISTENCE_MODE": {}, "SYSLOG_PACKAGE": {},
	"OTEL_EXPORTER_OTLP_ENDPOINT": {}, "OTEL_SERVICE_NAME": {}, "OTEL_LOGS_ENABLED": {}, "WEKA_CLI_DEBUG": {},
	"PYTHONPATH": {}, "NODE_NAME": {}, "MANAGEMENT_IP": {}, "POD_ID": {}, "POD_NAME": {}, "POD_NAMESPACE": {},
}

// comparedInitResourceNames are the init container resource names DiffPods compares, per side.
var comparedInitResourceNames = []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory}

// comparedFields are the single-valued pod fields DiffPods compares; c is the weka container.
var comparedFields = []struct {
	path  string
	value func(pod *corev1.Pod, c *corev1.Container) string
}{
	{"spec.hostNetwork", func(pod *corev1.Pod, _ *corev1.Container) string { return strconv.FormatBool(pod.Spec.HostNetwork) }},
	{"spec.serviceAccountName", func(pod *corev1.Pod, _ *corev1.Container) string { return pod.Spec.ServiceAccountName }},
	{"spec.dnsPolicy", func(pod *corev1.Pod, _ *corev1.Container) string { return string(pod.Spec.DNSPolicy) }},
	{"containers[0].command", func(_ *corev1.Pod, c *corev1.Container) string { return fmt.Sprintf("%q", c.Command) }},
	{"containers[0].securityContext.privileged", func(_ *corev1.Pod, c *corev1.Container) string {
		return strconv.FormatBool(c.SecurityContext != nil && ptr.Deref(c.SecurityContext.Privileged, false))
	}},
}

// DiffPods lists how live differs from expected, looking only at values the operator writes; anything else on
// live is ignored. The weka container is expected.Spec.Containers[0]. Entries are "<key> <live>→<expected>"
// ("<none>" for a missing side) and sorted.
func DiffPods(live, expected *corev1.Pod, additionalSecretKeys []string, skipCPU bool) []string {
	expectedC := &expected.Spec.Containers[0]
	liveC := &corev1.Container{}
	if i := slices.IndexFunc(live.Spec.Containers, func(c corev1.Container) bool { return c.Name == expectedC.Name }); i >= 0 {
		liveC = &live.Spec.Containers[i]
	}

	secretVolumes := make([]string, 0, len(additionalSecretKeys))
	for _, key := range additionalSecretKeys {
		secretVolumes = append(secretVolumes, additionalSecretVolumeName(key))
	}
	volumeCompared := func(name string) bool {
		if name == "node-info" { // reserved, but changes only by a direct WekaContainer edit
			return false
		}
		return IsReservedVolumeName(name) || slices.Contains(secretVolumes, name)
	}

	var diffs []string
	for _, f := range comparedFields {
		if l, e := f.value(live, liveC), f.value(expected, expectedC); l != e {
			diffs = append(diffs, fmt.Sprintf("%s %s→%s", f.path, l, e))
		}
	}

	liveHash, liveOK := live.Annotations[consts.PodConfigHashAnnotation]
	expectedHash, expectedOK := expected.Annotations[consts.PodConfigHashAnnotation]
	if liveOK && expectedOK && liveHash != expectedHash {
		diffs = append(diffs, fmt.Sprintf("podConfigHash %s→%s", liveHash, expectedHash))
	}

	envName := func(e corev1.EnvVar) string { return e.Name }
	envCompared := func(e corev1.EnvVar) bool { _, ok := comparedEnvNames[e.Name]; return ok }
	diffs = append(diffs, diffKeyed("",
		indexBy(liveC.Env, envName, envCompared), indexBy(expectedC.Env, envName, envCompared),
		func(a, b corev1.EnvVar) bool { return envValue(a) == envValue(b) }, envValue)...)

	volumeName := func(v corev1.Volume) string { return v.Name }
	volumeIsCompared := func(v corev1.Volume) bool { return volumeCompared(v.Name) }
	diffs = append(diffs, diffKeyed("volume ",
		indexBy(live.Spec.Volumes, volumeName, volumeIsCompared), indexBy(expected.Spec.Volumes, volumeName, volumeIsCompared),
		func(a, b corev1.Volume) bool { return equality.Semantic.DeepEqual(a.VolumeSource, b.VolumeSource) }, present)...)

	mountPath := func(m corev1.VolumeMount) string { return m.MountPath }
	mountCompared := func(m corev1.VolumeMount) bool { return volumeCompared(m.Name) }
	diffs = append(diffs, diffKeyed("mount ",
		indexBy(liveC.VolumeMounts, mountPath, mountCompared), indexBy(expectedC.VolumeMounts, mountPath, mountCompared),
		semanticEqual, func(m corev1.VolumeMount) string { return m.Name })...)

	comparedRequest := func(name corev1.ResourceName) bool { return comparedResource(name, true, skipCPU) }
	comparedLimit := func(name corev1.ResourceName) bool { return comparedResource(name, false, skipCPU) }
	diffs = append(diffs, diffKeyed("requests.",
		quantities(liveC.Resources.Requests, comparedRequest), quantities(expectedC.Resources.Requests, comparedRequest),
		resource.Quantity.Equal, quantityString)...)
	diffs = append(diffs, diffKeyed("limits.",
		quantities(liveC.Resources.Limits, comparedLimit), quantities(expectedC.Resources.Limits, comparedLimit),
		resource.Quantity.Equal, quantityString)...)

	claimName := func(c corev1.ResourceClaim) string { return c.Name }
	numaClaim := func(c corev1.ResourceClaim) bool { return c.Name == consts.WekaNumaClaimName }
	diffs = append(diffs, diffKeyed("claim ",
		indexBy(liveC.Resources.Claims, claimName, numaClaim), indexBy(expectedC.Resources.Claims, claimName, numaClaim),
		semanticEqual, present)...)

	podClaimName := func(c corev1.PodResourceClaim) string { return c.Name }
	numaPodClaim := func(c corev1.PodResourceClaim) bool { return c.Name == consts.WekaNumaClaimName }
	diffs = append(diffs, diffKeyed("resourceClaim ",
		indexBy(live.Spec.ResourceClaims, podClaimName, numaPodClaim), indexBy(expected.Spec.ResourceClaims, podClaimName, numaPodClaim),
		semanticEqual, present)...)

	initName := func(c corev1.Container) string { return c.Name }
	initCompared := func(c corev1.Container) bool { return c.Name == otelInstallerContainerName }
	diffs = append(diffs, diffKeyed("initContainer ",
		indexBy(live.Spec.InitContainers, initName, initCompared), indexBy(expected.Spec.InitContainers, initName, initCompared),
		func(a, b corev1.Container) bool { return initContainerEqual(&a, &b, volumeCompared) }, present)...)

	slices.Sort(diffs)
	return diffs
}

// initContainerEqual compares the init container sub-fields the operator writes. The API server also mounts the
// service-account token into init containers and a LimitRange can add resource names, so only allow-listed mounts
// and comparedInitResourceNames are compared.
func initContainerEqual(a, b *corev1.Container, volumeCompared func(string) bool) bool {
	compared := func(mounts []corev1.VolumeMount) []corev1.VolumeMount {
		return slices.DeleteFunc(slices.Clone(mounts), func(m corev1.VolumeMount) bool { return !volumeCompared(m.Name) })
	}
	sameQuantities := func(x, y corev1.ResourceList) bool {
		return maps.EqualFunc(quantities(x, comparedInitResource), quantities(y, comparedInitResource), resource.Quantity.Equal)
	}
	return equality.Semantic.DeepEqual(a.Command, b.Command) &&
		sameQuantities(a.Resources.Requests, b.Resources.Requests) &&
		sameQuantities(a.Resources.Limits, b.Resources.Limits) &&
		equality.Semantic.DeepEqual(compared(a.VolumeMounts), compared(b.VolumeMounts))
}

func comparedInitResource(name corev1.ResourceName) bool {
	return slices.Contains(comparedInitResourceNames, name)
}

// comparedResource reports whether DiffPods compares a weka container resource; the API server and a LimitRange
// may add the rest. ephemeral-storage is written to requests only.
func comparedResource(name corev1.ResourceName, request, skipCPU bool) bool {
	switch name {
	case corev1.ResourceCPU:
		return !skipCPU
	case corev1.ResourceEphemeralStorage:
		return request
	case corev1.ResourceMemory, resourceHugepages2Mi, consts.ResourceDrives, domain.WEKANICs:
		return true
	}
	return strings.HasPrefix(string(name), consts.WekaNumaRegionResourcePrefix)
}

func semanticEqual[V any](a, b V) bool { return equality.Semantic.DeepEqual(a, b) }

func quantityString(q resource.Quantity) string { return q.String() }

func present[V any](V) string { return "present" }

func quantities(list corev1.ResourceList, keep func(corev1.ResourceName) bool) map[string]resource.Quantity {
	m := make(map[string]resource.Quantity, len(list))
	for name, q := range list {
		if keep(name) {
			m[string(name)] = q
		}
	}
	return m
}

// envValue is the compared value of an env var; the API server adds fieldRef.apiVersion, so only the path counts.
func envValue(e corev1.EnvVar) string {
	if e.ValueFrom != nil && e.ValueFrom.FieldRef != nil {
		return "fieldRef:" + e.ValueFrom.FieldRef.FieldPath
	}
	if e.Value == "" {
		return `""`
	}
	return e.Value
}

// indexBy maps the items keep accepts (all when nil) by key.
func indexBy[T any](items []T, key func(T) string, keep func(T) bool) map[string]T {
	m := make(map[string]T, len(items))
	for _, item := range items {
		if keep == nil || keep(item) {
			m[key(item)] = item
		}
	}
	return m
}

// diffKeyed reports every key present on either side whose values differ. show renders a value; unequal values
// that render the same are reported as "changed".
func diffKeyed[V any](prefix string, live, expected map[string]V, equal func(a, b V) bool, show func(V) string) []string {
	keys := make(map[string]struct{}, len(live)+len(expected))
	for k := range live {
		keys[k] = struct{}{}
	}
	for k := range expected {
		keys[k] = struct{}{}
	}

	render := func(v V, ok bool) string {
		if !ok {
			return "<none>"
		}
		return show(v)
	}

	var diffs []string
	for k := range keys {
		l, liveOK := live[k]
		e, expectedOK := expected[k]
		switch {
		case liveOK && expectedOK && equal(l, e):
		case liveOK && expectedOK && show(l) == show(e):
			diffs = append(diffs, fmt.Sprintf("%s%s changed", prefix, k))
		default:
			diffs = append(diffs, fmt.Sprintf("%s%s %s→%s", prefix, k, render(l, liveOK), render(e, expectedOK)))
		}
	}
	return diffs
}
