package admission

import (
	"context"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
)

// FinalizerProtectionHandler is a raw admission handler that blocks
// removal of protected finalizers by non-operator service accounts.
// Unlike other validators, it has NO objectSelector escape hatch.
type FinalizerProtectionHandler struct {
	decoder admission.Decoder
}

// protectedFinalizers is the set of finalizers that may only be removed
// by the operator's own service account.
var protectedFinalizers = []string{
	consts.WekaFinalizer,
}

func (h *FinalizerProtectionHandler) Handle(_ context.Context, req admission.Request) admission.Response { //nolint:gocritic // req is admission.Handler's fixed interface signature, cannot pass by pointer
	// Only guard UPDATE; allow everything else unconditionally.
	if req.Operation != admissionv1.Update {
		return admission.Allowed("")
	}

	oldMeta := &metav1.PartialObjectMetadata{}
	if err := h.decoder.DecodeRaw(req.OldObject, oldMeta); err != nil {
		return admission.Errored(http.StatusBadRequest,
			fmt.Errorf("failed to decode old object metadata: %w", err))
	}

	newMeta := &metav1.PartialObjectMetadata{}
	if err := h.decoder.DecodeRaw(req.Object, newMeta); err != nil {
		return admission.Errored(http.StatusBadRequest,
			fmt.Errorf("failed to decode new object metadata: %w", err))
	}

	removed := removedFinalizers(oldMeta.Finalizers, newMeta.Finalizers)
	if len(removed) == 0 {
		return admission.Allowed("")
	}

	// Check whether the caller is the operator's own SA.
	if config.Config.OperatorPodNamespace == "" || config.Config.OperatorServiceAccountName == "" {
		return admission.Denied("operator namespace or service account name is not configured; cannot verify caller identity")
	}
	expected := fmt.Sprintf("system:serviceaccount:%s:%s",
		config.Config.OperatorPodNamespace, config.Config.OperatorServiceAccountName)
	if req.UserInfo.Username == expected {
		return admission.Allowed("")
	}

	// Non-operator caller removed a protected finalizer — deny.
	return admission.Denied(fmt.Sprintf(
		"removal of finalizer(s) %v is not allowed; they are managed by the weka-operator",
		removed))
}

// removedFinalizers returns protected finalizers present in old but absent in new.
func removedFinalizers(old, current []string) []string {
	newSet := make(map[string]struct{}, len(current))
	for _, f := range current {
		newSet[f] = struct{}{}
	}

	var removed []string
	for _, f := range old {
		if _, ok := newSet[f]; ok {
			continue
		}
		for _, pf := range protectedFinalizers {
			if f == pf {
				removed = append(removed, f)
				break
			}
		}
	}
	return removed
}

// RegisterFinalizerProtectionWebhook registers the finalizer-protection
// admission handler with the manager's webhook server.
func RegisterFinalizerProtectionWebhook(mgr ctrl.Manager) {
	mgr.GetWebhookServer().Register(FinalizerProtectionWebhookPath, &webhook.Admission{
		Handler: &FinalizerProtectionHandler{
			decoder: admission.NewDecoder(mgr.GetScheme()),
		},
	})
}
