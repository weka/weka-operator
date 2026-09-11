package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// extraVolumesCase is run against both clusterExtraVolumes and
// clientExtraVolumes: the two are thin wrappers over the same rule body
// (validateExtraVolumes), so one table exercises every rule for both.
type extraVolumesCase struct {
	name       string
	volumesRaw string // JSON for extraVolumes; "" leaves it unset
	mounts     []corev1.VolumeMount
	wantErrs   int
}

func extraVolumesCases() []extraVolumesCase {
	return []extraVolumesCase{
		{name: "unset extraVolumes, no mounts", wantErrs: 0},
		{name: "empty extraVolumes array", volumesRaw: `[]`, wantErrs: 0},
		{
			name:       "unknown field rejected",
			volumesRaw: `[{"name":"ca","mountPatch":"typo","secret":{"secretName":"ca"}}]`,
			wantErrs:   1,
		},
		{
			name:       "empty name",
			volumesRaw: `[{"name":"","secret":{"secretName":"ca"}}]`,
			wantErrs:   1,
		},
		{
			name:       "invalid DNS-1123 name",
			volumesRaw: `[{"name":"Not_Valid","secret":{"secretName":"ca"}}]`,
			wantErrs:   1,
		},
		{
			name: "duplicate names",
			volumesRaw: `[{"name":"ca","secret":{"secretName":"ca1"}},
				{"name":"ca","secret":{"secretName":"ca2"}}]`,
			wantErrs: 1,
		},
		{
			name:       "reserved name",
			volumesRaw: `[{"name":"dev","emptyDir":{}}]`,
			wantErrs:   1,
		},
		{
			// wekahome-cacert-secret is the one literal name AdditionalSecrets derives
			// (pod.go); a plausible user name like foo-secret must NOT be caught by this.
			name:       "reserved derived secret volume name",
			volumesRaw: `[{"name":"wekahome-cacert-secret","secret":{"secretName":"ca"}}]`,
			wantErrs:   1,
		},
		{
			name:       "non-reserved -secret suffix is accepted",
			volumesRaw: `[{"name":"foo-secret","secret":{"secretName":"ca"}}]`,
			wantErrs:   0,
		},
		{
			name:       "mount names an undeclared volume",
			volumesRaw: `[{"name":"ca","secret":{"secretName":"ca"}}]`,
			mounts:     []corev1.VolumeMount{{Name: "other", MountPath: "/etc/ssl/certs/corp-ca.crt"}},
			wantErrs:   1,
		},
		{
			name:       "relative mount path",
			volumesRaw: `[{"name":"ca","secret":{"secretName":"ca"}}]`,
			mounts:     []corev1.VolumeMount{{Name: "ca", MountPath: "etc/ssl/certs/corp-ca.crt"}},
			wantErrs:   1,
		},
		{
			name:       "uncleaned mount path",
			volumesRaw: `[{"name":"ca","secret":{"secretName":"ca"}}]`,
			mounts:     []corev1.VolumeMount{{Name: "ca", MountPath: "/etc/ssl/../ssl/certs/corp-ca.crt"}},
			wantErrs:   1,
		},
		{
			name: "duplicate mount path",
			volumesRaw: `[{"name":"ca","secret":{"secretName":"ca1"}},
				{"name":"ca2","secret":{"secretName":"ca2"}}]`,
			mounts: []corev1.VolumeMount{
				{Name: "ca", MountPath: "/etc/ssl/certs/corp-ca.crt"},
				{Name: "ca2", MountPath: "/etc/ssl/certs/corp-ca.crt"},
			},
			wantErrs: 1,
		},
		{
			name:       "reserved mount path",
			volumesRaw: `[{"name":"ca","secret":{"secretName":"ca"}}]`,
			mounts:     []corev1.VolumeMount{{Name: "ca", MountPath: "/opt/weka/foo"}},
			wantErrs:   1,
		},
		{
			// The motivating use case: mounting a CA bundle under /etc/ssl. If this
			// case fails, the feature has no reason to exist.
			name:       "valid CA bundle mount passes",
			volumesRaw: `[{"name":"corp-ca","secret":{"secretName":"corp-ca-bundle"}}]`,
			mounts: []corev1.VolumeMount{
				{Name: "corp-ca", MountPath: "/etc/ssl/certs/corp-ca.crt", SubPath: "ca.crt", ReadOnly: true},
			},
			wantErrs: 0,
		},
		{
			name: "multiple simultaneous violations",
			volumesRaw: `[{"name":"","secret":{"secretName":"a"}},
				{"name":"dev","secret":{"secretName":"b"}}]`,
			mounts: []corev1.VolumeMount{{Name: "missing", MountPath: "relative/path"}},
			// extraVolumes[0].name (empty), extraVolumes[1].name (reserved),
			// extraVolumeMounts[0].name (undeclared), extraVolumeMounts[0].mountPath (relative)
			wantErrs: 4,
		},
	}
}

// assertFieldPaths checks that errs covers exactly the field paths keyed in want, order
// ignored. want's values are unused; it doubles as the set of paths still to be seen.
func assertFieldPaths(t *testing.T, errs field.ErrorList, want map[string]bool) {
	t.Helper()
	if len(errs) != len(want) {
		t.Fatalf("expected %d violations, got %d: %v", len(want), len(errs), errs)
	}
	for _, e := range errs {
		if _, ok := want[e.Field]; !ok {
			t.Errorf("unexpected field path %q", e.Field)
			continue
		}
		want[e.Field] = true
	}
	for f, seen := range want {
		if !seen {
			t.Errorf("expected a violation at field path %q, got none", f)
		}
	}
}

func rawExtraVolumes(jsonStr string) *runtime.RawExtension {
	if jsonStr == "" {
		return nil
	}
	return &runtime.RawExtension{Raw: []byte(jsonStr)}
}

func TestClusterExtraVolumes(t *testing.T) {
	v := clusterExtraVolumes{}
	for _, tc := range extraVolumesCases() {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
			cluster.Spec.PodConfig = &weka.PodConfiguration{
				ExtraVolumes:      rawExtraVolumes(tc.volumesRaw),
				ExtraVolumeMounts: tc.mounts,
			}

			errs := v.Validate(context.Background(), nil, cluster)
			if len(errs) != tc.wantErrs {
				t.Fatalf("expected %d violations, got %d: %v", tc.wantErrs, len(errs), errs)
			}
			for _, e := range errs {
				if e.Field != "spec.podConfig.extraVolumes" &&
					!strings.HasPrefix(e.Field, "spec.podConfig.extraVolumes[") &&
					!strings.HasPrefix(e.Field, "spec.podConfig.extraVolumeMounts[") {
					t.Errorf("unexpected field path %q", e.Field)
				}
			}
		})
	}
}

func TestClusterExtraVolumes_NoPodConfig(t *testing.T) {
	v := clusterExtraVolumes{}
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
	if errs := v.Validate(context.Background(), nil, cluster); errs != nil {
		t.Fatalf("expected nil when podConfig is unset, got %v", errs)
	}
}

func TestClusterExtraVolumes_WrongType(t *testing.T) {
	v := clusterExtraVolumes{}
	if errs := v.Validate(context.Background(), nil, &weka.WekaClient{}); errs != nil {
		t.Fatalf("expected nil for non-WekaCluster object, got %v", errs)
	}
}

func TestClusterExtraVolumes_ViolationFieldPaths(t *testing.T) {
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
	cluster.Spec.PodConfig = &weka.PodConfiguration{
		ExtraVolumes: rawExtraVolumes(`[{"name":"","secret":{"secretName":"a"}},
			{"name":"dev","secret":{"secretName":"b"}}]`),
		ExtraVolumeMounts: []corev1.VolumeMount{{Name: "missing", MountPath: "relative/path"}},
	}

	v := clusterExtraVolumes{}
	errs := v.Validate(context.Background(), nil, cluster)

	wantFields := map[string]bool{
		"spec.podConfig.extraVolumes[0].name":           false,
		"spec.podConfig.extraVolumes[1].name":           false,
		"spec.podConfig.extraVolumeMounts[0].name":      false,
		"spec.podConfig.extraVolumeMounts[0].mountPath": false,
	}
	assertFieldPaths(t, errs, wantFields)
}

func TestClientExtraVolumes(t *testing.T) {
	v := clientExtraVolumes{}
	for _, tc := range extraVolumesCases() {
		t.Run(tc.name, func(t *testing.T) {
			wc := &weka.WekaClient{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
			wc.Spec.ExtraVolumes = rawExtraVolumes(tc.volumesRaw)
			wc.Spec.ExtraVolumeMounts = tc.mounts

			errs := v.Validate(context.Background(), nil, wc)
			if len(errs) != tc.wantErrs {
				t.Fatalf("expected %d violations, got %d: %v", tc.wantErrs, len(errs), errs)
			}
			for _, e := range errs {
				if e.Field != "spec.extraVolumes" &&
					!strings.HasPrefix(e.Field, "spec.extraVolumes[") &&
					!strings.HasPrefix(e.Field, "spec.extraVolumeMounts[") {
					t.Errorf("unexpected field path %q", e.Field)
				}
			}
		})
	}
}

func TestClientExtraVolumes_WrongType(t *testing.T) {
	v := clientExtraVolumes{}
	if errs := v.Validate(context.Background(), nil, &weka.WekaCluster{}); errs != nil {
		t.Fatalf("expected nil for non-WekaClient object, got %v", errs)
	}
}

func TestClientExtraVolumes_ViolationFieldPaths(t *testing.T) {
	wc := &weka.WekaClient{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
	wc.Spec.ExtraVolumes = rawExtraVolumes(`[{"name":"","secret":{"secretName":"a"}},
		{"name":"dev","secret":{"secretName":"b"}}]`)
	wc.Spec.ExtraVolumeMounts = []corev1.VolumeMount{{Name: "missing", MountPath: "relative/path"}}

	v := clientExtraVolumes{}
	errs := v.Validate(context.Background(), nil, wc)

	wantFields := map[string]bool{
		"spec.extraVolumes[0].name":           false,
		"spec.extraVolumes[1].name":           false,
		"spec.extraVolumeMounts[0].name":      false,
		"spec.extraVolumeMounts[0].mountPath": false,
	}
	assertFieldPaths(t, errs, wantFields)
}
