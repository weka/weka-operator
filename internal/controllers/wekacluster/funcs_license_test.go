package wekacluster

import (
	"bytes"
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1/condition"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/pkg/util/podexec"
)

type recordingExec struct{ cmds []string }

func (e *recordingExec) Exec(context.Context, []string) (bytes.Buffer, bytes.Buffer, error) {
	panic("unexpected Exec")
}

func (e *recordingExec) ExecNamed(context.Context, string, []string) (bytes.Buffer, bytes.Buffer, error) {
	panic("unexpected ExecNamed")
}

func (e *recordingExec) ExecSensitive(_ context.Context, _ string, cmd []string) (bytes.Buffer, bytes.Buffer, error) {
	e.cmds = append(e.cmds, strings.Join(cmd, " "))
	return bytes.Buffer{}, bytes.Buffer{}, nil
}

type fakeExecService struct {
	exec.ExecService
	rec *recordingExec
}

func (f fakeExecService) GetExecutor(context.Context, *weka.WekaContainer) (podexec.Exec, error) {
	return f.rec, nil
}

func newLicenseLoop(t *testing.T, secret *corev1.Secret) (*wekaClusterReconcilerLoop, *recordingExec) {
	t.Helper()
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
		Spec:       weka.WekaClusterSpec{LicenseSecret: "weka-license"},
	}
	loop := newUpgradeLoop(t, cluster, []*weka.WekaContainer{operationalContainer("c1", 0)})
	if secret != nil {
		if err := loop.getClient().Create(context.Background(), secret); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recordingExec{}
	loop.ExecService = fakeExecService{rec: rec}
	return loop, rec
}

func licenseSecret(uid types.UID, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "weka-license", Namespace: "ns", UID: uid},
		Data:       data,
	}
}

func TestEnsureLicense_ReappliesOnlyWhenSecretVersionChanges(t *testing.T) {
	ctx := context.Background()
	secret := licenseSecret("uid-1", map[string][]byte{"license": []byte("LIC1\n")})
	loop, rec := newLicenseLoop(t, secret)

	if err := loop.EnsureLicense(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rec.cmds) != 1 || !strings.Contains(rec.cmds[0], "weka cluster license set LIC1") {
		t.Fatalf("first apply: cmds = %q", rec.cmds)
	}

	if err := loop.EnsureLicense(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rec.cmds) != 1 {
		t.Fatalf("unchanged secret re-applied the license: cmds = %q", rec.cmds)
	}

	secret.Data["license"] = []byte("LIC2")
	if err := loop.getClient().Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if err := loop.EnsureLicense(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rec.cmds) != 2 {
		t.Fatalf("changed resourceVersion did not re-apply: cmds = %q", rec.cmds)
	}

	// Recreated Secret: the condition still records the old UID, so a new UID must re-apply.
	cond := meta.FindStatusCondition(loop.cluster.Status.Conditions, condition.CondLicenseApplied)
	cond.Message = "uid-0/" + secret.ResourceVersion
	if err := loop.EnsureLicense(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rec.cmds) != 3 {
		t.Fatalf("new UID did not re-apply: cmds = %q", rec.cmds)
	}
	if cond := meta.FindStatusCondition(loop.cluster.Status.Conditions, condition.CondLicenseApplied); cond.Message != "uid-1/"+secret.ResourceVersion {
		t.Fatalf("condition message = %q", cond.Message)
	}
}

func TestEnsureLicense_MissingKeyFails(t *testing.T) {
	loop, rec := newLicenseLoop(t, licenseSecret("uid-1", map[string][]byte{"other": []byte("x")}))

	if err := loop.EnsureLicense(context.Background()); err == nil {
		t.Fatal("expected error for secret without license key")
	}
	if len(rec.cmds) != 0 {
		t.Fatalf("license applied despite missing key: cmds = %q", rec.cmds)
	}
	cond := meta.FindStatusCondition(loop.cluster.Status.Conditions, condition.CondLicenseApplied)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("LicenseApplied condition = %+v, want False", cond)
	}
}

func TestEnsureLicense_WiredIntoReconcileLoop(t *testing.T) {
	cluster := &weka.WekaCluster{Spec: weka.WekaClusterSpec{LicenseSecret: "weka-license"}}
	step := findStep((&wekaClusterReconcilerLoop{cluster: cluster}).GetAllSteps(), "EnsureLicense")
	if step == nil || !stepPredicatesPass(step) {
		t.Fatal("EnsureLicense is not wired into GetAllSteps or its predicate does not pass")
	}
	cluster.Spec.LicenseSecret = ""
	if stepPredicatesPass(step) {
		t.Fatal("EnsureLicense predicate passes without spec.licenseSecret")
	}
}
