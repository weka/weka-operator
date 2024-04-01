package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("WekaClusterStatus", func() {
	Describe("InitStatus", func() {
		var status WekaClusterStatus
		BeforeEach(func() {
			status = WekaClusterStatus{}
		})
		Context("before initialization", func() {
			It("should have no conditions", func() {
				Expect(status.Conditions).To(BeEmpty())
			})
		})

		When("initialized", func() {
			BeforeEach(func() {
				status.InitStatus()
			})

			It("should create the conditions", func() {
				Expect(status.Conditions).NotTo(BeEmpty())
			})

			It("should not initialize the conditions", func() {
				for _, cond := range status.Conditions {
					Expect(cond.Status).To(Equal(v1.ConditionFalse))
					Expect(cond.Reason).To(Equal("Init"))
				}
			})
		})
	})
})
