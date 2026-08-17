package wekacontainer

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("handleCleanupDiscoveryWait", func() {
	var (
		scheme    *runtime.Scheme
		container *weka.WekaContainer
		recorder  *record.FakeRecorder
		waitErr   error
	)

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(weka.AddToScheme(scheme)).To(Succeed())

		container = &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default"},
			Spec:       weka.WekaContainerSpec{NodeAffinity: "node1"},
		}
		recorder = record.NewFakeRecorder(10)
		waitErr = lifecycle.NewWaitError(stderrors.New("container execution result is not ready"))
	})

	newReconciler := func() *containerReconcilerLoop {
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(container).
			WithStatusSubresource(container).
			Build()
		return &containerReconcilerLoop{
			Client:        fakeClient,
			container:     container,
			Recorder:      recorder,
			ThrottlingMap: throttling.NewSyncMapThrottler(),
		}
	}

	It("stamps the first wait once, returns the error unchanged, and records no event", func() {
		r := newReconciler()

		err := r.handleCleanupDiscoveryWait(context.Background(), waitErr)

		Expect(err).To(Equal(waitErr))
		Expect(container.Status.Timestamps).To(HaveKey(persistentDirCleanupStartedKey))
		Expect(recorder.Events).ToNot(Receive())
	})

	It("leaves a fresh stamp untouched and keeps waiting quietly", func() {
		started := metav1.Time{Time: time.Now().Add(-1 * time.Minute)}
		container.Status.Timestamps = map[string]metav1.Time{persistentDirCleanupStartedKey: started}
		r := newReconciler()

		err := r.handleCleanupDiscoveryWait(context.Background(), waitErr)

		Expect(err).To(Equal(waitErr))
		Expect(container.Status.Timestamps[persistentDirCleanupStartedKey]).To(Equal(started))
		Expect(recorder.Events).ToNot(Receive())
	})

	It("emits a warning event and slows the requeue once past the timeout", func() {
		started := metav1.Time{Time: time.Now().Add(-11 * time.Minute)}
		container.Status.Timestamps = map[string]metav1.Time{persistentDirCleanupStartedKey: started}
		r := newReconciler()

		err := r.handleCleanupDiscoveryWait(context.Background(), waitErr)

		var event string
		Expect(recorder.Events).To(Receive(&event))
		Expect(event).To(ContainSubstring("Warning"))
		Expect(event).To(ContainSubstring("PersistentDirCleanupStuck"))

		we, ok := err.(*lifecycle.WaitError)
		Expect(ok).To(BeTrue(), "expected a *lifecycle.WaitError, got %T", err)
		Expect(we.Duration).To(Equal(persistentDirCleanupSlowRequeue))
	})
})

// TestIsWaitError exercises detection against the real wrapping ExecuteOperation produces: PollResults
// returns a raw *lifecycle.WaitError, which the nested steps engine (run via AsRunFunc) wraps in a
// *lifecycle.StepRunError, which the outer engine (ExecuteOperation itself) wraps in a second
// *lifecycle.StepRunError — neither StepRunError implements Unwrap.
func TestIsWaitError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "raw wait error",
			err:  lifecycle.NewWaitError(stderrors.New("waiting")),
			want: true,
		},
		{
			name: "wrapped as ExecuteOperation actually wraps it (two StepRunError layers around WaitError)",
			err: &lifecycle.StepRunError{
				Step: &lifecycle.SimpleStep{Name: "DiscoverNode"},
				Err: &lifecycle.StepRunError{
					Step: &lifecycle.SimpleStep{Name: "PollResults"},
					Err:  lifecycle.NewWaitError(stderrors.New("container execution result is not ready")),
				},
			},
			want: true,
		},
		{
			name: "plain error",
			err:  stderrors.New("boom"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isWaitError(tt.err); got != tt.want {
				t.Fatalf("isWaitError() = %v, want %v", got, tt.want)
			}
		})
	}
}
