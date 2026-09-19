package utils

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const distServiceURL = "http://weka-drivers-dist.default.svc:8080"

// distPolicy builds a policy that has deployed the dist service. withType controls whether
// spec.type is set: it is optional, so a policy carrying only driverDistPayload is valid and must
// still be matched here.
func distPolicy(name string, withType bool) *weka.WekaPolicy {
	policy := &weka.WekaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: weka.WekaPolicySpec{
			Payload: weka.PolicyPayload{DriverDistPayload: &weka.DriverDistPayload{}},
		},
		Status: weka.WekaPolicyStatus{
			TypedStatus: &weka.TypedPolicyStatus{
				DistService: &weka.DistServiceStatus{ServiceUrl: distServiceURL},
			},
		},
	}
	if withType {
		policy.Spec.Type = weka.WekaPolicyTypeEnableLocalDriversDistribution
	}
	return policy
}

func distScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("add weka scheme: %v", err)
	}
	return scheme
}

func TestResolveDriversDistService(t *testing.T) {
	cases := []struct {
		name       string
		objects    []client.Object
		explicit   string
		wantURL    string
		wantSource DriverDistSource
	}{
		{
			name:       "no policy falls back to the public endpoint",
			wantURL:    "https://drivers.weka.io",
			wantSource: DriverDistDefault,
		},
		{
			name:       "explicit value wins",
			objects:    []client.Object{distPolicy("dist", true)},
			explicit:   "http://mirror.internal",
			wantURL:    "http://mirror.internal",
			wantSource: DriverDistExplicit,
		},
		{
			name:       "policy with an explicit spec.type",
			objects:    []client.Object{distPolicy("dist", true)},
			wantURL:    distServiceURL,
			wantSource: DriverDistPolicy,
		},
		{
			// spec.type is optional, so the type has to be derived from the payload. Reading
			// spec.type literally leaves this policy unmatched and silently serves the default.
			name:       "policy without spec.type is still matched",
			objects:    []client.Object{distPolicy("dist", false)},
			wantURL:    distServiceURL,
			wantSource: DriverDistPolicy,
		},
		{
			name: "two policies are ambiguous, keep the default",
			objects: []client.Object{
				distPolicy("dist-a", false),
				distPolicy("dist-b", true),
			},
			wantURL:    "https://drivers.weka.io",
			wantSource: DriverDistAmbiguous,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(distScheme(t)).WithObjects(tc.objects...).Build()

			url, source := ResolveDriversDistService(context.Background(), c, "default", tc.explicit)
			if url != tc.wantURL {
				t.Errorf("url = %q, want %q", url, tc.wantURL)
			}
			if source != tc.wantSource {
				t.Errorf("source = %v, want %v", source, tc.wantSource)
			}
		})
	}
}
