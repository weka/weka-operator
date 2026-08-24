package wekacluster

import (
	"regexp"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"gopkg.in/yaml.v3"

	"github.com/weka/weka-operator/pkg/util"
)

// managementProxyLoop builds a minimal reconciler loop for generateEnvoyConfig, which only reads
// r.cluster.Status.Ports. No client/Manager is needed since the function does no I/O.
func managementProxyLoop(basePort, proxyPort int) *wekaClusterReconcilerLoop {
	cluster := &weka.WekaCluster{}
	cluster.Status.Ports.BasePort = basePort
	cluster.Status.Ports.ManagementProxyPort = proxyPort
	return &wekaClusterReconcilerLoop{cluster: cluster}
}

// proxyContainerWithIP builds a backend container carrying a single management IP, the only field
// generateEnvoyConfig reads off it.
func proxyContainerWithIP(name, ip string) *weka.WekaContainer {
	c := &weka.WekaContainer{}
	c.Name = name
	c.Status.ManagementIPs = []string{ip}
	return c
}

func defaultProxySettings() managementProxySettings {
	return managementProxySettings{
		Replicas:              2,
		HealthyPanicThreshold: 0,
		AdminBindAddress:      "127.0.0.1",
	}
}

// ipv4AddressField matches an "address:" YAML field carrying a dotted-quad IPv4 literal, tolerating
// either a quoted or bare value.
var ipv4AddressField = regexp.MustCompile(`address:\s*"?(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})"?`)

// TestGenerateEnvoyConfig_BootstrapCarriesNoEndpointIPs pins the whole point of filesystem EDS: the
// bootstrap must be endpoint-IP-free so its hash (and thus the pod template) never changes on
// backend churn. Loopback/wildcard binds are legitimate address: values and must not trip this.
func TestGenerateEnvoyConfig_BootstrapCarriesNoEndpointIPs(t *testing.T) {
	loop := managementProxyLoop(14000, 15000)
	containers := []*weka.WekaContainer{
		proxyContainerWithIP("c1", "10.1.2.3"),
		proxyContainerWithIP("c2", "10.1.2.4"),
	}

	bootstrap, _, err := loop.generateEnvoyConfig(containers, defaultProxySettings())
	if err != nil {
		t.Fatalf("generateEnvoyConfig returned error: %v", err)
	}

	for _, m := range ipv4AddressField.FindAllStringSubmatch(bootstrap, -1) {
		ip := m[1]
		if ip == "0.0.0.0" || ip == defaultProxySettings().AdminBindAddress {
			continue
		}
		t.Errorf("bootstrap contains a non-listener/admin IPv4 address %q; endpoints must live only in eds.yaml:\n%s", ip, bootstrap)
	}
}

// TestGenerateEnvoyConfig_ClusterNameMatchesServiceName guards the silent-failure mode hit in live
// testing: if eds.yaml's cluster_name doesn't match the bootstrap's eds_cluster_config.service_name,
// Envoy rejects the EDS update and keeps serving stale endpoints with no outward symptom.
func TestGenerateEnvoyConfig_ClusterNameMatchesServiceName(t *testing.T) {
	loop := managementProxyLoop(14000, 15000)
	containers := []*weka.WekaContainer{proxyContainerWithIP("c1", "10.1.2.3")}

	bootstrap, eds, err := loop.generateEnvoyConfig(containers, defaultProxySettings())
	if err != nil {
		t.Fatalf("generateEnvoyConfig returned error: %v", err)
	}

	var bootstrapDoc struct {
		StaticResources struct {
			Clusters []struct {
				Name             string `yaml:"name"`
				EDSClusterConfig struct {
					ServiceName string `yaml:"service_name"`
				} `yaml:"eds_cluster_config"`
			} `yaml:"clusters"`
		} `yaml:"static_resources"`
	}
	if err := yaml.Unmarshal([]byte(bootstrap), &bootstrapDoc); err != nil {
		t.Fatalf("bootstrap did not parse as YAML: %v\n%s", err, bootstrap)
	}
	if len(bootstrapDoc.StaticResources.Clusters) != 1 {
		t.Fatalf("expected exactly one cluster in bootstrap, got %d", len(bootstrapDoc.StaticResources.Clusters))
	}
	serviceName := bootstrapDoc.StaticResources.Clusters[0].EDSClusterConfig.ServiceName

	var edsDoc struct {
		Resources []struct {
			ClusterName string `yaml:"cluster_name"`
		} `yaml:"resources"`
	}
	if err := yaml.Unmarshal([]byte(eds), &edsDoc); err != nil {
		t.Fatalf("eds.yaml did not parse as YAML: %v\n%s", err, eds)
	}
	if len(edsDoc.Resources) != 1 {
		t.Fatalf("expected exactly one resource in eds.yaml, got %d", len(edsDoc.Resources))
	}

	if serviceName != wekaBackendClusterName {
		t.Errorf("bootstrap eds_cluster_config.service_name = %q, want %q", serviceName, wekaBackendClusterName)
	}
	if edsDoc.Resources[0].ClusterName != wekaBackendClusterName {
		t.Errorf("eds.yaml resources[0].cluster_name = %q, want %q", edsDoc.Resources[0].ClusterName, wekaBackendClusterName)
	}
	if edsDoc.Resources[0].ClusterName != serviceName {
		t.Errorf("eds.yaml cluster_name %q does not match bootstrap service_name %q; Envoy would reject this EDS update", edsDoc.Resources[0].ClusterName, serviceName)
	}
}

// TestGenerateEnvoyConfig_HashStability pins EnvoyConfigHashAnnotation's whole reason to exist:
// endpoint churn alone must never roll the proxy pods, but any bootstrap-affecting setting must.
func TestGenerateEnvoyConfig_HashStability(t *testing.T) {
	settings := defaultProxySettings()

	// Takes its own *testing.T: subtests below call this, and t.Fatalf on the parent from a subtest
	// reports against the wrong test.
	bootstrapHash := func(t *testing.T, loop *wekaClusterReconcilerLoop, containers []*weka.WekaContainer, s managementProxySettings) string {
		t.Helper()
		bootstrap, _, err := loop.generateEnvoyConfig(containers, s)
		if err != nil {
			t.Fatalf("generateEnvoyConfig returned error: %v", err)
		}
		return util.GetHash(bootstrap, 8)
	}

	baseLoop := managementProxyLoop(14000, 15000)
	endpointsA := []*weka.WekaContainer{
		proxyContainerWithIP("c1", "10.1.2.3"),
		proxyContainerWithIP("c2", "10.1.2.4"),
	}
	endpointsB := []*weka.WekaContainer{
		proxyContainerWithIP("c1", "10.9.9.9"),
		proxyContainerWithIP("c2", "10.9.9.10"),
		proxyContainerWithIP("c3", "10.9.9.11"),
	}

	hashA := bootstrapHash(t, baseLoop, endpointsA, settings)
	hashB := bootstrapHash(t, baseLoop, endpointsB, settings)
	if hashA != hashB {
		t.Errorf("bootstrap hash changed with endpoint set (different IPs and count): %q vs %q; endpoint churn must not roll the proxy pods", hashA, hashB)
	}

	t.Run("proxy port change flips the hash", func(t *testing.T) {
		otherPortLoop := managementProxyLoop(14000, 15001)
		hashOtherPort := bootstrapHash(t, otherPortLoop, endpointsA, settings)
		if hashOtherPort == hashA {
			t.Errorf("bootstrap hash unchanged after ManagementProxyPort change; got %q for both", hashA)
		}
	})

	t.Run("admin bind address change flips the hash", func(t *testing.T) {
		otherSettings := settings
		otherSettings.AdminBindAddress = "0.0.0.0"
		otherSettings.adminIsIPv6Wildcard = true // drives the bootstrap ipv4_compat branch
		hashOtherAdmin := bootstrapHash(t, baseLoop, endpointsA, otherSettings)
		if hashOtherAdmin == hashA {
			t.Errorf("bootstrap hash unchanged after AdminBindAddress change; got %q for both", hashA)
		}
	})

	t.Run("healthy panic threshold change flips the hash", func(t *testing.T) {
		otherSettings := settings
		otherSettings.HealthyPanicThreshold = 50
		hashOtherThreshold := bootstrapHash(t, baseLoop, endpointsA, otherSettings)
		if hashOtherThreshold == hashA {
			t.Errorf("bootstrap hash unchanged after HealthyPanicThreshold change; got %q for both", hashA)
		}
	})
}

// TestGenerateEnvoyConfig_EmptyEndpointsIsError guards against Envoy binding a listener with zero
// lb_endpoints: it accepts that and resets every connection while the TCP readiness probe still
// passes, so this must be a hard error rather than a silently-empty eds.yaml.
func TestGenerateEnvoyConfig_EmptyEndpointsIsError(t *testing.T) {
	loop := managementProxyLoop(14000, 15000)

	t.Run("no containers at all", func(t *testing.T) {
		bootstrap, eds, err := loop.generateEnvoyConfig(nil, defaultProxySettings())
		if err == nil {
			t.Fatalf("expected error for empty container list, got bootstrap=%q eds=%q", bootstrap, eds)
		}
		if eds != "" {
			t.Errorf("expected no eds.yaml on error, got %q", eds)
		}
	})

	t.Run("containers present but none has a usable management IP", func(t *testing.T) {
		noIP := &weka.WekaContainer{}
		noIP.Name = "no-ip"
		badIP := &weka.WekaContainer{}
		badIP.Name = "bad-ip"
		badIP.Status.ManagementIPs = []string{"not-an-ip"}

		bootstrap, eds, err := loop.generateEnvoyConfig([]*weka.WekaContainer{noIP, badIP}, defaultProxySettings())
		if err == nil {
			t.Fatalf("expected error when no container has a usable management IP, got bootstrap=%q eds=%q", bootstrap, eds)
		}
		if eds != "" {
			t.Errorf("expected no eds.yaml on error, got %q", eds)
		}
	})
}

// TestGenerateEnvoyConfig_BootstrapDeclaresEDSWiring guards the two live-testing failure modes
// tied to bootstrap wiring: a missing node id/cluster crash-loops Envoy under any xDS source, and a
// wrong EDS path/watched_directory leaves it never picking up eds.yaml.
func TestGenerateEnvoyConfig_BootstrapDeclaresEDSWiring(t *testing.T) {
	loop := managementProxyLoop(14000, 15000)
	containers := []*weka.WekaContainer{proxyContainerWithIP("c1", "10.1.2.3")}

	bootstrap, _, err := loop.generateEnvoyConfig(containers, defaultProxySettings())
	if err != nil {
		t.Fatalf("generateEnvoyConfig returned error: %v", err)
	}

	var doc struct {
		Node struct {
			ID      string `yaml:"id"`
			Cluster string `yaml:"cluster"`
		} `yaml:"node"`
		StaticResources struct {
			Clusters []struct {
				Type             string `yaml:"type"`
				EDSClusterConfig struct {
					EDSConfig struct {
						PathConfigSource struct {
							Path             string `yaml:"path"`
							WatchedDirectory struct {
								Path string `yaml:"path"`
							} `yaml:"watched_directory"`
						} `yaml:"path_config_source"`
					} `yaml:"eds_config"`
				} `yaml:"eds_cluster_config"`
			} `yaml:"clusters"`
		} `yaml:"static_resources"`
	}
	if err := yaml.Unmarshal([]byte(bootstrap), &doc); err != nil {
		t.Fatalf("bootstrap did not parse as YAML: %v\n%s", err, bootstrap)
	}

	if doc.Node.ID == "" || doc.Node.Cluster == "" {
		t.Errorf("expected non-empty node.id and node.cluster, got id=%q cluster=%q", doc.Node.ID, doc.Node.Cluster)
	}

	if len(doc.StaticResources.Clusters) != 1 {
		t.Fatalf("expected exactly one cluster, got %d", len(doc.StaticResources.Clusters))
	}
	cluster := doc.StaticResources.Clusters[0]
	if cluster.Type != "EDS" {
		t.Errorf("cluster type = %q, want %q", cluster.Type, "EDS")
	}
	path := cluster.EDSClusterConfig.EDSConfig.PathConfigSource.Path
	if path != "/etc/envoy/eds.yaml" {
		t.Errorf("eds_config.path_config_source.path = %q, want %q", path, "/etc/envoy/eds.yaml")
	}
	watchedDir := cluster.EDSClusterConfig.EDSConfig.PathConfigSource.WatchedDirectory.Path
	if watchedDir != "/etc/envoy" {
		t.Errorf("watched_directory.path = %q, want %q", watchedDir, "/etc/envoy")
	}
}

// TestGenerateEnvoyConfig_EDSIsWellFormedDiscoveryResponse parses eds.yaml as Envoy would and checks
// the rendered endpoints round-trip the input set exactly (same addresses and ports).
func TestGenerateEnvoyConfig_EDSIsWellFormedDiscoveryResponse(t *testing.T) {
	const basePort = 14000
	loop := managementProxyLoop(basePort, 15000)
	containers := []*weka.WekaContainer{
		proxyContainerWithIP("c1", "10.1.2.3"),
		proxyContainerWithIP("c2", "10.1.2.4"),
		proxyContainerWithIP("c3", "10.1.2.5"),
	}

	_, eds, err := loop.generateEnvoyConfig(containers, defaultProxySettings())
	if err != nil {
		t.Fatalf("generateEnvoyConfig returned error: %v", err)
	}

	var doc struct {
		VersionInfo string `yaml:"version_info"`
		Resources   []struct {
			Type      string `yaml:"@type"`
			Endpoints []struct {
				LBEndpoints []struct {
					Endpoint struct {
						Address struct {
							SocketAddress struct {
								Address string `yaml:"address"`
								Port    int    `yaml:"port_value"`
							} `yaml:"socket_address"`
						} `yaml:"address"`
					} `yaml:"endpoint"`
				} `yaml:"lb_endpoints"`
			} `yaml:"endpoints"`
		} `yaml:"resources"`
	}
	if err := yaml.Unmarshal([]byte(eds), &doc); err != nil {
		t.Fatalf("eds.yaml did not parse as YAML: %v\n%s", err, eds)
	}

	if doc.VersionInfo == "" {
		t.Error("expected non-empty version_info")
	}
	if len(doc.Resources) != 1 {
		t.Fatalf("expected exactly one resource, got %d", len(doc.Resources))
	}
	const wantType = "type.googleapis.com/envoy.config.endpoint.v3.ClusterLoadAssignment"
	if doc.Resources[0].Type != wantType {
		t.Errorf("resources[0][\"@type\"] = %q, want %q", doc.Resources[0].Type, wantType)
	}
	if len(doc.Resources[0].Endpoints) != 1 {
		t.Fatalf("expected exactly one endpoints group, got %d", len(doc.Resources[0].Endpoints))
	}

	gotAddrs := make(map[string]bool)
	for _, ep := range doc.Resources[0].Endpoints[0].LBEndpoints {
		sa := ep.Endpoint.Address.SocketAddress
		if sa.Port != basePort {
			t.Errorf("endpoint %q rendered with port %d, want %d", sa.Address, sa.Port, basePort)
		}
		gotAddrs[sa.Address] = true
	}

	wantAddrs := map[string]bool{"10.1.2.3": true, "10.1.2.4": true, "10.1.2.5": true}
	if len(gotAddrs) != len(wantAddrs) {
		t.Fatalf("got %d distinct endpoint addresses, want %d: got=%v", len(gotAddrs), len(wantAddrs), gotAddrs)
	}
	for addr := range wantAddrs {
		if !gotAddrs[addr] {
			t.Errorf("expected endpoint address %q missing from rendered eds.yaml", addr)
		}
	}
}
