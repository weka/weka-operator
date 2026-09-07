package wekacluster

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
)

// fakeOverrideService embeds the interface so only the override methods need bodies; any other
// call panics, which is the point: this step must touch nothing else.
type fakeOverrideService struct {
	services.WekaService
	entries []services.WekaOverride
	added   []string
	removed []string
}

func (f *fakeOverrideService) ListOverridesByKey(_ context.Context, key string) ([]services.WekaOverride, error) {
	var out []services.WekaOverride
	for _, e := range f.entries {
		if e.Key == key {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeOverrideService) AddOverride(_ context.Context, key, value, _ string, _ bool) error {
	f.added = append(f.added, key+"="+value)
	return nil
}

func (f *fakeOverrideService) RemoveOverride(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	return nil
}

func TestReconcileWekaHomeCacertOverride(t *testing.T) {
	set := services.WekaOverride{OverrideID: "7", Key: "weka_cloud_ca_cert_path", Value: domain.WekaHomeCacertPath, Enabled: true}
	foreign := services.WekaOverride{OverrideID: "9", Key: "weka_cloud_ca_cert_path", Value: "/etc/ssl/corp-ca.pem", Enabled: true}
	for _, tc := range []struct {
		name        string
		secret      string
		entries     []services.WekaOverride
		wantAdded   int
		wantRemoved []string
	}{
		{name: "secret set, override missing: added", secret: "wh-ca", wantAdded: 1},
		{name: "secret set, override present: no-op", secret: "wh-ca", entries: []services.WekaOverride{set}},
		{name: "secret cleared, override present: removed", entries: []services.WekaOverride{set}, wantRemoved: []string{"7"}},
		{name: "secret cleared, nothing set: no-op"},
		// ensureOverride compares only the tail value and force-adds on mismatch, so a row left
		// by hand or by an older operator is shadowed, not replaced. weka honours the last row.
		{name: "secret set, override present with a stale value: re-added, stale row left", secret: "wh-ca",
			entries:   []services.WekaOverride{{OverrideID: "7", Key: "weka_cloud_ca_cert_path", Value: "/etc/ssl/other.pem", Enabled: true}},
			wantAdded: 1},
		// A row pointing somewhere else is someone else's (hand-set, or a CA mounted through
		// extraVolumes): removing it would silently drop the cluster back to the OS trust store.
		{name: "secret cleared, foreign override present: left alone", entries: []services.WekaOverride{foreign}},
		{name: "secret cleared, ours and a foreign row present: only ours removed",
			entries:     []services.WekaOverride{foreign, set},
			wantRemoved: []string{"7"}},
		// A disabled row carries the right value but no effect, so it must be re-added rather than
		// reported as already set.
		{name: "secret set, override present but disabled: re-added", secret: "wh-ca",
			entries:   []services.WekaOverride{{OverrideID: "7", Key: "weka_cloud_ca_cert_path", Value: domain.WekaHomeCacertPath}},
			wantAdded: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeOverrideService{entries: tc.entries}
			r := &wekaClusterReconcilerLoop{}
			if err := r.reconcileWekaHomeCacertOverride(context.Background(), svc, tc.secret); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(svc.added) != tc.wantAdded {
				t.Errorf("added %v, want %d adds", svc.added, tc.wantAdded)
			}
			if len(svc.removed) != len(tc.wantRemoved) || (len(tc.wantRemoved) > 0 && svc.removed[0] != tc.wantRemoved[0]) {
				t.Errorf("removed %v, want %v", svc.removed, tc.wantRemoved)
			}
		})
	}
}

func TestCacertSecretForOverride(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  weka.WekaHomeConfig
		want string
	}{
		{"endpoint and secret set", weka.WekaHomeConfig{Endpoint: "https://wh", CacertSecret: "wh-ca"}, "wh-ca"},
		{"endpoint empty, secret set", weka.WekaHomeConfig{Endpoint: "", CacertSecret: "wh-ca"}, ""},
		{"endpoint set, secret empty", weka.WekaHomeConfig{Endpoint: "https://wh", CacertSecret: ""}, ""},
		{"insecure TLS keeps the secret", weka.WekaHomeConfig{Endpoint: "https://wh", CacertSecret: "wh-ca", AllowInsecure: true}, "wh-ca"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacertSecretForOverride(tc.cfg); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
