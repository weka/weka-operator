package services

import (
	"context"
	"errors"
	"testing"
	"time"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
)

func boolPtr(b bool) *bool { return &b }

// failingListClient stands in for an unsynced informer at startup or a cancelled reconcile context.
type failingListClient struct {
	client.Client
	lists int
}

func (c *failingListClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	c.lists++
	return errors.New("cache not synced")
}

func configPolicy(name, namespace string, forceBuilderCli bool) *weka.WekaPolicy {
	return &weka.WekaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: weka.WekaPolicySpec{
			Payload: weka.PolicyPayload{
				Configuration: &weka.ConfigurationPayload{
					Drivers: &weka.DriversSpec{ForceBuilderCli: boolPtr(forceBuilderCli)},
				},
			},
		},
	}
}

// invalidConfigPolicy combines a configuration payload with a runnable one, which GetType rejects.
func invalidConfigPolicy(name, namespace string) *weka.WekaPolicy {
	policy := configPolicy(name, namespace, true)
	policy.Spec.Payload.SignDrives = &weka.SignDrivesPayload{}
	return policy
}

func testScheme(t *testing.T) *runtime.Scheme {
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

// useNamespace pins the operator namespace so util.GetPodNamespace resolves inside a test process.
func useNamespace(t *testing.T, namespace string) {
	t.Helper()
	previous := config.Config.OperatorPodNamespace
	config.Config.OperatorPodNamespace = namespace
	t.Cleanup(func() { config.Config.OperatorPodNamespace = previous })
}

func clientWith(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objects...).Build()
}

func TestSettingsFromPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload *weka.ConfigurationPayload
		want    bool
	}{
		{"nil payload keeps the default", nil, false},
		{"nil drivers section keeps the default", &weka.ConfigurationPayload{}, false},
		{"nil field keeps the default", &weka.ConfigurationPayload{Drivers: &weka.DriversSpec{}}, false},
		{"explicit false", &weka.ConfigurationPayload{Drivers: &weka.DriversSpec{ForceBuilderCli: boolPtr(false)}}, false},
		{"explicit true", &weka.ConfigurationPayload{Drivers: &weka.DriversSpec{ForceBuilderCli: boolPtr(true)}}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SettingsFromPayload(tc.payload).Drivers.ForceBuilderCli; got != tc.want {
				t.Errorf("ForceBuilderCli = %v, want %v", got, tc.want)
			}
		})
	}
}

// Resolution has exactly three outcomes: a single valid policy applies, no policy at all means the
// built-in defaults, and anything else means no usable configuration.
func TestResolveConfigurationSettings(t *testing.T) {
	useNamespace(t, "weka-operator-system")
	ctx := context.Background()

	t.Run("one valid policy applies", func(t *testing.T) {
		got, err := resolveConfigurationSettings(ctx, clientWith(t, configPolicy("configuration", "weka-operator-system", true)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Drivers.ForceBuilderCli {
			t.Error("ForceBuilderCli = false, want the policy value")
		}
	})

	t.Run("no policy is not an error", func(t *testing.T) {
		got, err := resolveConfigurationSettings(ctx, clientWith(t))
		if err != nil {
			t.Fatalf("no configuration policy must resolve to defaults, got error: %v", err)
		}
		if got != DefaultConfigurationSettings() {
			t.Errorf("got %+v, want the built-in defaults", got)
		}
	})

	t.Run("a policy in a tenant namespace is not seen", func(t *testing.T) {
		got, err := resolveConfigurationSettings(ctx, clientWith(t, configPolicy("configuration", "tenant-a", true)))
		if err != nil || got.Drivers.ForceBuilderCli {
			t.Errorf("tenant policy leaked into install-wide settings: %+v, err=%v", got, err)
		}
	})

	t.Run("two policies cannot be chosen between", func(t *testing.T) {
		_, err := resolveConfigurationSettings(ctx, clientWith(t,
			configPolicy("configuration-a", "weka-operator-system", true),
			configPolicy("configuration-b", "weka-operator-system", false),
		))
		if err == nil {
			t.Error("expected an error for two configuration policies")
		}
	})

	t.Run("a policy the controller rejects is an error, not a fallback", func(t *testing.T) {
		_, err := resolveConfigurationSettings(ctx, clientWith(t, invalidConfigPolicy("broken", "weka-operator-system")))
		if err == nil {
			t.Error("expected an error for a policy GetType rejects")
		}
	})

	t.Run("a malformed policy of another kind is ignored", func(t *testing.T) {
		// two runnable payloads - GetType rejects it, but it is not a configuration policy, so it
		// is the policy controller's problem and must not make configuration unavailable
		other := &weka.WekaPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "weka-operator-system"},
			Spec: weka.WekaPolicySpec{Payload: weka.PolicyPayload{
				SignDrives:     &weka.SignDrivesPayload{},
				DiscoverDrives: &weka.DiscoverDrivesPayload{},
			}},
		}
		if _, err := resolveConfigurationSettings(ctx, clientWith(t, other)); err != nil {
			t.Errorf("an unrelated malformed policy must not break configuration: %v", err)
		}
	})

	t.Run("an unreadable list is an error", func(t *testing.T) {
		if _, err := resolveConfigurationSettings(ctx, &failingListClient{Client: clientWith(t)}); err == nil {
			t.Error("expected an error when the list fails")
		}
	})
}

func TestGetSettingsServesResolvedValue(t *testing.T) {
	useNamespace(t, "weka-operator-system")
	svc := newSettingsService()

	svc.refresh(context.Background(), clientWith(t, configPolicy("configuration", "weka-operator-system", true)))

	if got := svc.get(context.Background()).Drivers.ForceBuilderCli; !got {
		t.Error("ForceBuilderCli = false, want the resolved value")
	}
}

// A failure must not serve anything: no built-in defaults, and no previously resolved value. That
// substitution is what makes a running operator behave differently from a restarted one.
func TestGetSettingsBlocksWhenUnresolvable(t *testing.T) {
	useNamespace(t, "weka-operator-system")
	svc := newSettingsService()

	// a good value first, so the test proves the previous value is dropped rather than never set
	svc.refresh(context.Background(), clientWith(t, configPolicy("configuration", "weka-operator-system", true)))
	if !svc.get(context.Background()).Drivers.ForceBuilderCli {
		t.Fatal("seed resolution did not take")
	}

	svc.refresh(context.Background(), &failingListClient{Client: clientWith(t)})

	// Known-bad must fail immediately. Callers run on a shared reconcile worker pool, so parking
	// one for settingsWaitTimeout would starve every other object the controller serves.
	defer func() {
		if recover() == nil {
			t.Error("expected a panic: after a failed resolution there is nothing safe to serve")
		}
	}()

	start := time.Now()
	defer func() {
		if waited := time.Since(start); waited > time.Second {
			t.Errorf("blocked for %s on a known-bad configuration; it must not wait", waited)
		}
	}()

	svc.get(context.Background())
}

// Before anything has been resolved there is nothing to fail fast on, so a caller waits out the
// startup race rather than panicking on a configuration that may be moments from being readable.
func TestGetSettingsWaitsBeforeTheFirstAttempt(t *testing.T) {
	useNamespace(t, "weka-operator-system")
	svc := newSettingsService()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	defer func() {
		if recover() == nil {
			t.Error("expected a panic once the context ended while still uninitialised")
		}
	}()

	start := time.Now()
	defer func() {
		if waited := time.Since(start); waited < 50*time.Millisecond {
			t.Errorf("returned after %s; an uninitialised service must wait for the first resolution", waited)
		}
	}()

	svc.get(ctx)
}

// Recovery is what the background refresh is for: nothing else would retry after a failure.
func TestGetSettingsRecoversAfterTransientFailure(t *testing.T) {
	useNamespace(t, "weka-operator-system")
	svc := newSettingsService()
	good := clientWith(t, configPolicy("configuration", "weka-operator-system", true))

	svc.refresh(context.Background(), &failingListClient{Client: good})
	svc.refresh(context.Background(), good)

	if got := svc.get(context.Background()).Drivers.ForceBuilderCli; !got {
		t.Error("a later successful resolution must unblock callers")
	}
}
