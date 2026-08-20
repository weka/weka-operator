package validation

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Validator is one admission rule. ID is the stable key used by Helm
// overrides and the defaults table; once a policy ships it must not
// change. Validate returns one *field.Error per violation; nil = pass.
type Validator interface {
	ID() string
	Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList
}

// UpdateValidator is an admission rule that requires both the old and new
// object. Used for checks that detect a regression between revisions (e.g.
// a field that must not decrease). Only invoked on Update operations.
type UpdateValidator interface {
	ID() string
	ValidateUpdate(ctx context.Context, c client.Client, oldObj, newObj runtime.Object) field.ErrorList
}
