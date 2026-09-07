package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	env "github.com/weka/weka-operator/internal/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestClientWekahomeCacertUsable(t *testing.T) {
	v := &clientWekahomeCacertUsable{}

	newClient := func(cacertSecret string, target weka.ObjectReference) *weka.WekaClient {
		wc := &weka.WekaClient{ObjectMeta: metav1.ObjectMeta{Name: "wc", Namespace: "client-ns"}}
		wc.Spec.TargetCluster = target
		if cacertSecret != "" {
			wc.Spec.WekaHome = &weka.WekahomeClientConfig{CacertSecret: cacertSecret}
		}
		return wc
	}
	newSecret := func(name string, data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "client-ns"},
			Data:       data,
		}
	}
	goodData := func() map[string][]byte { return map[string][]byte{"ca.crt": selfSignedCertPEM(t)} }
	sameNsCluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "client-ns"},
		Spec:       weka.WekaClusterSpec{WekaHome: &weka.WekaHomeConfig{CacertSecret: "inherited-ca"}},
	}
	crossNsCluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "cluster-ns"},
		Spec:       weka.WekaClusterSpec{WekaHome: &weka.WekaHomeConfig{CacertSecret: "inherited-ca"}},
	}

	for _, tc := range []struct {
		name         string
		obj          runtime.Object
		seed         []client.Object
		wantErrs     int
		wantContains string
	}{
		{name: "nothing set anywhere", obj: newClient("", weka.ObjectReference{})},
		{
			name:         "own secret missing",
			obj:          newClient("my-ca", weka.ObjectReference{}),
			wantErrs:     1,
			wantContains: "will not start",
		},
		{
			name:     "own secret holds junk",
			obj:      newClient("my-ca", weka.ObjectReference{}),
			seed:     []client.Object{newSecret("my-ca", map[string][]byte{"ca.crt": []byte("not a certificate")})},
			wantErrs: 1,
		},
		{
			name: "own secret usable",
			obj:  newClient("my-ca", weka.ObjectReference{}),
			seed: []client.Object{newSecret("my-ca", goodData())},
		},
		// Resolution, not the spec field: a same-namespace cluster's secret is what gets mounted.
		{
			name:     "secret inherited from same-namespace cluster holds only a key",
			obj:      newClient("", weka.ObjectReference{Name: "cluster", Namespace: "client-ns"}),
			seed:     []client.Object{sameNsCluster, newSecret("inherited-ca", map[string][]byte{"tls.key": selfSignedKeyPEM(t)})},
			wantErrs: 1,
		},
		{
			name: "secret inherited from same-namespace cluster is usable",
			obj:  newClient("", weka.ObjectReference{Name: "cluster", Namespace: "client-ns"}),
			seed: []client.Object{sameNsCluster, newSecret("inherited-ca", goodData())},
		},
		// Cross-namespace is not inherited (client_wekahome_cacert_not_inherited warns about that),
		// so nothing resolves and there is nothing to check.
		{
			name: "cross-namespace cluster secret is not inherited",
			obj:  newClient("", weka.ObjectReference{Name: "cluster", Namespace: "cluster-ns"}),
			seed: []client.Object{crossNsCluster},
		},
		{name: "wrong type", obj: &weka.WekaCluster{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := v.Validate(context.Background(), cacertFakeClient(t, tc.seed...), tc.obj)
			if len(errs) != tc.wantErrs {
				t.Fatalf("expected %d errors, got %v", tc.wantErrs, errs)
			}
			if tc.wantContains != "" && !strings.Contains(errs[0].Detail, tc.wantContains) {
				t.Errorf("expected Detail to contain %q, got %q", tc.wantContains, errs[0].Detail)
			}
		})
	}
}

// The operator-wide default reaches clients that set no wekaHome block at all, so an unusable one
// is exactly the case a user gets no other signal about.
func TestClientWekahomeCacertUsable_OperatorWideDefault(t *testing.T) {
	v := &clientWekahomeCacertUsable{}
	wc := &weka.WekaClient{ObjectMeta: metav1.ObjectMeta{Name: "wc", Namespace: "client-ns"}}

	saved := env.Config.WekaHome.CacertSecret
	defer func() { env.Config.WekaHome.CacertSecret = saved }()
	env.Config.WekaHome.CacertSecret = "corp-ca"

	errs := v.Validate(context.Background(), cacertFakeClient(t), wc)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error for a default secret missing in the namespace, got %v", errs)
	}
	if errs[0].Field != "spec.wekaHome.cacertSecret" {
		t.Errorf("unexpected field path %q", errs[0].Field)
	}
}
