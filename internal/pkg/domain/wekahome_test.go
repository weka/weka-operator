package domain

import (
	"testing"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	env "github.com/weka/weka-operator/internal/config"
)

func TestGetWekaHomeClientCacertSecret(t *testing.T) {
	origCacertSecret := env.Config.WekaHome.CacertSecret
	defer func() { env.Config.WekaHome.CacertSecret = origCacertSecret }()

	tests := []struct {
		name                      string
		client                    *v1alpha1.WekaClient
		targetCluster             *v1alpha1.WekaCluster
		envCacertSecret           string
		wantSecret                string
		wantCrossNamespaceSkipped bool
	}{
		{
			name:          "client-only topology, no explicit value, no env default",
			client:        &v1alpha1.WekaClient{},
			targetCluster: nil,
			wantSecret:    "",
		},
		{
			name: "client-only topology, explicit client value",
			client: &v1alpha1.WekaClient{Spec: v1alpha1.WekaClientSpec{
				WekaHome: &v1alpha1.WekahomeClientConfig{CacertSecret: "client-secret"},
			}},
			targetCluster: nil,
			wantSecret:    "client-secret",
		},
		{
			name: "explicit client value wins over cluster value",
			client: &v1alpha1.WekaClient{Spec: v1alpha1.WekaClientSpec{
				WekaHome: &v1alpha1.WekahomeClientConfig{CacertSecret: "client-secret"},
			}},
			targetCluster: &v1alpha1.WekaCluster{Spec: v1alpha1.WekaClusterSpec{
				WekaHome: &v1alpha1.WekaHomeConfig{CacertSecret: "cluster-secret"},
			}},
			wantSecret: "client-secret",
		},
		{
			name:   "cluster value used when client sets nothing",
			client: &v1alpha1.WekaClient{},
			targetCluster: &v1alpha1.WekaCluster{Spec: v1alpha1.WekaClusterSpec{
				WekaHome: &v1alpha1.WekaHomeConfig{CacertSecret: "cluster-secret"},
			}},
			wantSecret: "cluster-secret",
		},
		{
			name:            "env default used when neither client nor cluster set it",
			client:          &v1alpha1.WekaClient{},
			targetCluster:   &v1alpha1.WekaCluster{},
			envCacertSecret: "env-secret",
			wantSecret:      "env-secret",
		},
		{
			name: "spec.wekaHome entirely absent still gets the env default (defect-1 regression)",
			client: &v1alpha1.WekaClient{Spec: v1alpha1.WekaClientSpec{
				WekaHome: nil,
			}},
			targetCluster:   nil,
			envCacertSecret: "env-secret",
			wantSecret:      "env-secret",
		},
		{
			name: "client value wins when it matches the cluster",
			client: &v1alpha1.WekaClient{Spec: v1alpha1.WekaClientSpec{
				WekaHome: &v1alpha1.WekahomeClientConfig{CacertSecret: "same-secret"},
			}},
			targetCluster: &v1alpha1.WekaCluster{Spec: v1alpha1.WekaClusterSpec{
				WekaHome: &v1alpha1.WekaHomeConfig{CacertSecret: "same-secret"},
			}},
			wantSecret: "same-secret",
		},
		{
			name: "client value used when cluster has none",
			client: &v1alpha1.WekaClient{Spec: v1alpha1.WekaClientSpec{
				WekaHome: &v1alpha1.WekahomeClientConfig{CacertSecret: "client-secret"},
			}},
			targetCluster: &v1alpha1.WekaCluster{},
			wantSecret:    "client-secret",
		},
		{
			name:   "cluster value inherited when client has none",
			client: &v1alpha1.WekaClient{},
			targetCluster: &v1alpha1.WekaCluster{Spec: v1alpha1.WekaClusterSpec{
				WekaHome: &v1alpha1.WekaHomeConfig{CacertSecret: "cluster-secret"},
			}},
			wantSecret: "cluster-secret",
		},
		{
			// GetWekahomeConfig folds the env default into the cluster's resolved CacertSecret, but
			// divergence must be judged against the cluster's *own* explicit value - a client overriding
			// an operator-wide default it never disagreed with the cluster about is not a divergence.
			name: "client overrides env default, cluster sets nothing: no false divergence",
			client: &v1alpha1.WekaClient{Spec: v1alpha1.WekaClientSpec{
				WekaHome: &v1alpha1.WekahomeClientConfig{CacertSecret: "client-ca"},
			}},
			targetCluster:   &v1alpha1.WekaCluster{},
			envCacertSecret: "global-ca",
			wantSecret:      "client-ca",
		},
		{
			name:            "neither client nor cluster set it: env default used, no divergence",
			client:          &v1alpha1.WekaClient{},
			targetCluster:   &v1alpha1.WekaCluster{},
			envCacertSecret: "global-ca",
			wantSecret:      "global-ca",
		},
		{
			name:   "cross-namespace cluster cacertSecret is not inherited",
			client: &v1alpha1.WekaClient{ObjectMeta: metav1.ObjectMeta{Namespace: "clients-ns"}},
			targetCluster: &v1alpha1.WekaCluster{
				ObjectMeta: metav1.ObjectMeta{Namespace: "cluster-ns"},
				Spec:       v1alpha1.WekaClusterSpec{WekaHome: &v1alpha1.WekaHomeConfig{CacertSecret: "cluster-secret"}},
			},
			wantSecret:                "",
			wantCrossNamespaceSkipped: true,
		},
		{
			name:   "same-namespace cluster cacertSecret is still inherited",
			client: &v1alpha1.WekaClient{ObjectMeta: metav1.ObjectMeta{Namespace: "shared-ns"}},
			targetCluster: &v1alpha1.WekaCluster{
				ObjectMeta: metav1.ObjectMeta{Namespace: "shared-ns"},
				Spec:       v1alpha1.WekaClusterSpec{WekaHome: &v1alpha1.WekaHomeConfig{CacertSecret: "cluster-secret"}},
			},
			wantSecret:                "cluster-secret",
			wantCrossNamespaceSkipped: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env.Config.WekaHome.CacertSecret = tt.envCacertSecret

			secret, crossNamespaceSkipped := GetWekaHomeClientCacertSecret(tt.client, tt.targetCluster)
			if secret != tt.wantSecret {
				t.Errorf("secret = %q, want %q", secret, tt.wantSecret)
			}
			if crossNamespaceSkipped != tt.wantCrossNamespaceSkipped {
				t.Errorf("crossNamespaceSkipped = %v, want %v", crossNamespaceSkipped, tt.wantCrossNamespaceSkipped)
			}
		})
	}
}
