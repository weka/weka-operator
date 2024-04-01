/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"

	"github.com/kr/pretty"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func createNamespace(ctx context.Context, name string) {
	key := client.ObjectKey{Name: name}
	namespace := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	if err := k8sClient.Get(ctx, key, namespace); err != nil {
		if apierrors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		}
	}
	Eventually(func() bool {
		err := k8sClient.Get(ctx, key, namespace)
		return err == nil
	})
}

func deleteNamespace(ctx context.Context, name string) {
	key := client.ObjectKey{Name: name}
	namespace := &v1.Namespace{}
	Expect(k8sClient.Get(ctx, key, namespace)).To(Succeed())
	Expect(k8sClient.Delete(ctx, namespace)).To(Succeed())
	Eventually(func() bool {
		err := k8sClient.Get(ctx, key, namespace)
		return apierrors.IsNotFound(err)
	})
}

func testingCluster() *wekav1alpha1.WekaCluster {
	key := client.ObjectKey{Name: "test-cluster", Namespace: "default"}
	cluster := &wekav1alpha1.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
		Spec: wekav1alpha1.WekaClusterSpec{
			Size:     1,
			Template: "test-template",
			Topology: "discover_oci",
			Image:    "test-image",
		},
	}
	return cluster
}

var _ = Describe("WekaCluster Controller", func() {
	var ctx context.Context
	BeforeEach(func() {
		ctx = context.Background()

		Expect(k8sManager).NotTo(BeNil())
	})

	_ = Describe("NewWekaClusterReconciler", func() {
		var subject *WekaClusterReconciler
		BeforeEach(func() {
			Expect(k8sManager).NotTo(BeNil())
			subject = NewWekaClusterController(k8sManager)
		})
		It("should return a new WekaClusterReconciler", func() {
			Expect(subject).NotTo(BeNil())
			Expect(subject.Client).NotTo(BeNil())
			Expect(subject.Scheme).NotTo(BeNil())
			Expect(subject.Logger).NotTo(BeNil())
			Expect(subject.Recorder).NotTo(BeNil())
		})
	})

	Describe("WekaClusterReconciler", func() {
		Context("with a cluster", func() {
			var key client.ObjectKey
			BeforeEach(func() {
				Expect(k8sManager).NotTo(BeNil())
				createNamespace(ctx, "weka-operator-system")

				key = client.ObjectKey{Name: "test-cluster", Namespace: "default"}
				cluster := &wekav1alpha1.WekaCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      key.Name,
						Namespace: key.Namespace,
					},
					Spec: wekav1alpha1.WekaClusterSpec{
						Size:     1,
						Template: "test-template",
						Topology: "discover_oci",
						Image:    "test-image",
					},
				}

				Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
				Eventually(func() error {
					return k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)
				}).Should(Succeed())
			})

			AfterEach(func() {
				Expect(key.Name).NotTo(BeEmpty())
				cluster := &wekav1alpha1.WekaCluster{}
				if err := k8sClient.Get(ctx, key, cluster); err == nil {
					Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
				}

				Eventually(func() bool {
					err := k8sClient.Get(ctx, key, cluster)
					return apierrors.IsNotFound(err)
				}).Should(BeTrue())
				deleteNamespace(ctx, "weka-operator-system")
			})

			It("should set a finalizer", func() {
				cluster := &wekav1alpha1.WekaCluster{}
				Eventually(func() bool {
					Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
					return controllerutil.ContainsFinalizer(cluster, WekaFinalizer)
				}).Should(BeTrue())
			})

			It("should initialize the status", func() {
				cluster := &wekav1alpha1.WekaCluster{}
				Eventually(func() bool {
					Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
					return len(cluster.Status.Conditions) > 0
				}).Should(BeTrue())
			})

			It("should create pods", func() {
				cluster := &wekav1alpha1.WekaCluster{}
				Eventually(func() bool {
					Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
					return len(cluster.Status.Conditions) > 0
				}).Should(BeTrue())

				var podsCreatedCondition *metav1.Condition
				Eventually(func() metav1.ConditionStatus {
					Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
					podsCreatedCondition = meta.FindStatusCondition(cluster.Status.Conditions, condition.CondPodsCreated)
					return podsCreatedCondition.Status
				}).Should(Equal(metav1.ConditionTrue), pretty.Sprintf("%# v", podsCreatedCondition))

				Expect(podsCreatedCondition.Reason).To(Equal("Init"), pretty.Sprintf("%# v", podsCreatedCondition))
			})

			It("should generate login credentials", func() {
				cluster := &wekav1alpha1.WekaCluster{}
				Eventually(func() bool {
					Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
					return len(cluster.Status.Conditions) > 0
				}).Should(BeTrue())

				var clusterSecretsCreatedCondition *metav1.Condition
				Eventually(func() metav1.ConditionStatus {
					Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
					clusterSecretsCreatedCondition = meta.FindStatusCondition(cluster.Status.Conditions, condition.CondClusterSecretsCreated)
					return clusterSecretsCreatedCondition.Status
				}).Should(Equal(metav1.ConditionTrue))

				Expect(clusterSecretsCreatedCondition.Reason).To(Equal("Init"), pretty.Sprintf("%# v", clusterSecretsCreatedCondition))
			})

			Describe("When cluster is created", func() {
				It("should set the cluster created condition", func() {
					cluster := &wekav1alpha1.WekaCluster{}
					Eventually(func() bool {
						Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
						return len(cluster.Status.Conditions) > 0
					}).Should(BeTrue())

					var clusterCreatedCondition *metav1.Condition
					Eventually(func() string {
						Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
						clusterCreatedCondition = meta.FindStatusCondition(cluster.Status.Conditions, condition.CondClusterCreated)
						return clusterCreatedCondition.Reason
					}, "10s").Should(Equal("Error"), pretty.Sprintf("%# v", clusterCreatedCondition))

					Expect(clusterCreatedCondition.Reason).To(Equal("Error"), pretty.Sprintf("%# v", clusterCreatedCondition))
				})
			})

			Describe("CondDrivesAdded", func() {
				It("should not add drives in testing", func() {
					cluster := &wekav1alpha1.WekaCluster{}
					Eventually(func() bool {
						Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
						return len(cluster.Status.Conditions) > 0
					}).Should(BeTrue())

					var drivesAddedCondition *metav1.Condition
					Eventually(func() metav1.ConditionStatus {
						Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
						drivesAddedCondition = meta.FindStatusCondition(cluster.Status.Conditions, condition.CondDrivesAdded)
						return drivesAddedCondition.Status
					}, "10s").Should(Equal(metav1.ConditionFalse), pretty.Sprintf("%# v", drivesAddedCondition))

					Expect(drivesAddedCondition.Reason).To(Equal("Init"), pretty.Sprintf("%# v", drivesAddedCondition))
					Expect(drivesAddedCondition.Message).To(Equal("Drives are not added yet"), pretty.Sprintf("%# v", drivesAddedCondition))
				})
			})

			PDescribe("CondIoStarted", func() {
			})

			PDescribe("CondClusterSecretsApplied", func() {
				It("should set the cluster secrets applied condition", func() {
					cluster := &wekav1alpha1.WekaCluster{}
					Eventually(func() bool {
						Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
						return len(cluster.Status.Conditions) > 0
					}).Should(BeTrue())

					var clusterSecretsAppliedCondition *metav1.Condition
					Eventually(func() metav1.ConditionStatus {
						Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
						clusterSecretsAppliedCondition = meta.FindStatusCondition(
							cluster.Status.Conditions,
							condition.CondClusterSecretsApplied,
						)
						return clusterSecretsAppliedCondition.Status
					}, "10s").Should(Equal(metav1.ConditionTrue))

					Expect(clusterSecretsAppliedCondition.Reason).To(
						Equal("Init"),
						pretty.Sprintf("%# v", clusterSecretsAppliedCondition),
					)
				})
			})
		})

		Context("when deleting a cluster", func() {
			var key client.ObjectKey
			BeforeEach(func() {
				Expect(k8sManager).NotTo(BeNil())
				createNamespace(ctx, "weka-operator-system")

				cluster := testingCluster()
				key = client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}
				Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
				Eventually(func() error {
					return k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)
				}).Should(Succeed())
			})

			AfterEach(func() {
				Expect(key.Name).NotTo(BeEmpty())
				cluster := &wekav1alpha1.WekaCluster{}
				if err := k8sClient.Get(ctx, key, cluster); err == nil {
					Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
				}

				Eventually(func() bool {
					err := k8sClient.Get(ctx, key, cluster)
					return apierrors.IsNotFound(err)
				}).Should(BeTrue())
				deleteNamespace(ctx, "weka-operator-system")
			})

			It("should remove finalizer", func() {
				cluster := &wekav1alpha1.WekaCluster{}
				Eventually(func() bool {
					Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
					return controllerutil.ContainsFinalizer(cluster, WekaFinalizer)
				}).Should(BeTrue())

				Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())

				Eventually(func() bool {
					return apierrors.IsNotFound(k8sClient.Get(ctx, key, cluster))
				}).Should(BeTrue())
			})
		})
	})
})
