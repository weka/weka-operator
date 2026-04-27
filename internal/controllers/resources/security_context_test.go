package resources

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

func TestSecurityContext(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SecurityContext Suite")
}

func ptr[T any](v T) *T { return &v }

var (
	appArmorUnconfined     = &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined}
	appArmorRuntimeDefault = &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeRuntimeDefault}
)

var _ = DescribeTable("GetSecurityProfile",
	func(raw string, want *corev1.PodSecurityContext) {
		DeferCleanup(os.Unsetenv, "WEKA_POD_SECURITY_CONTEXT")
		Expect(os.Setenv("WEKA_POD_SECURITY_CONTEXT", raw)).To(Succeed())

		Expect(GetSecurityProfile()).To(Equal(want))
	},
	Entry("empty input returns nil", "", nil),
	Entry("empty object returns nil", "{}", nil),
	Entry("json null returns nil", "null", nil),
	Entry("invalid json returns nil (and logs error)", "{not json", nil),
	Entry("appArmorProfile Unconfined",
		`{"appArmorProfile":{"type":"Unconfined"}}`,
		&corev1.PodSecurityContext{AppArmorProfile: appArmorUnconfined}),
	Entry("appArmorProfile RuntimeDefault",
		`{"appArmorProfile":{"type":"RuntimeDefault"}}`,
		&corev1.PodSecurityContext{AppArmorProfile: appArmorRuntimeDefault}),
	Entry("non-appArmor fields pass through",
		`{"runAsUser":1000,"fsGroup":2000,"seccompProfile":{"type":"RuntimeDefault"}}`,
		&corev1.PodSecurityContext{
			RunAsUser:      ptr(int64(1000)),
			FSGroup:        ptr(int64(2000)),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		}),
	Entry("appArmor combined with other fields passes through",
		`{"appArmorProfile":{"type":"Unconfined"},"runAsUser":1000,"fsGroup":2000}`,
		&corev1.PodSecurityContext{
			AppArmorProfile: appArmorUnconfined,
			RunAsUser:       ptr(int64(1000)),
			FSGroup:         ptr(int64(2000)),
		}),
)

var _ = Describe("GetSecurityProfile call isolation", func() {
	AfterEach(func() {
		Expect(os.Unsetenv("WEKA_POD_SECURITY_CONTEXT")).To(Succeed())
	})

	It("returns an independent struct each call (no shared state)", func() {
		Expect(os.Setenv("WEKA_POD_SECURITY_CONTEXT", `{"appArmorProfile":{"type":"Unconfined"}}`)).To(Succeed())
		a := GetSecurityProfile()
		b := GetSecurityProfile()
		Expect(a).NotTo(BeIdenticalTo(b))

		a.AppArmorProfile.Type = corev1.AppArmorProfileTypeRuntimeDefault
		Expect(b.AppArmorProfile.Type).To(Equal(corev1.AppArmorProfileTypeUnconfined))
	})
})
