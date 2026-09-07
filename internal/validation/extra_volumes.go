package validation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/weka/weka-operator/internal/controllers/resources"
)

// validateExtraVolumes is the shared rule body behind clusterExtraVolumes and
// clientExtraVolumes. extraVolumes is schemaless
// (x-kubernetes-preserve-unknown-fields), so the API server does no validation
// of it at all; this is the only thing standing between a user and a
// silently-dropped typo, which is why unknown fields are rejected outright
// rather than ignored.
func validateExtraVolumes(raw *runtime.RawExtension, mounts []corev1.VolumeMount, basePath *field.Path) field.ErrorList {
	volumesPath := basePath.Child("extraVolumes")
	mountsPath := basePath.Child("extraVolumeMounts")

	if raw == nil || len(raw.Raw) == 0 {
		// No volumes declared, so every mount is by definition mounting something undeclared.
		return validateExtraVolumeMounts(nil, mounts, mountsPath)
	}

	var volumes []corev1.Volume
	dec := json.NewDecoder(bytes.NewReader(raw.Raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&volumes); err != nil {
		return field.ErrorList{field.Invalid(volumesPath, string(raw.Raw),
			fmt.Sprintf("does not parse as a list of pod volumes: %v", err))}
	}

	var errs field.ErrorList
	seenNames := make(map[string]struct{}, len(volumes))
	declaredNames := make(map[string]struct{}, len(volumes))

	for i, v := range volumes {
		namePath := volumesPath.Index(i).Child("name")

		if reasons := k8svalidation.IsDNS1123Label(v.Name); len(reasons) > 0 {
			errs = append(errs, field.Invalid(namePath, v.Name, strings.Join(reasons, ", ")))
		} else {
			if _, dup := seenNames[v.Name]; dup {
				errs = append(errs, field.Duplicate(namePath, v.Name))
			}
			seenNames[v.Name] = struct{}{}

			if resources.IsReservedVolumeName(v.Name) {
				errs = append(errs, field.Invalid(namePath, v.Name,
					"is reserved for an operator-managed volume"))
			}
		}
		// Recorded even when invalid, so a mount naming this entry isn't also flagged
		// as pointing at nothing - the name error above is enough.
		declaredNames[v.Name] = struct{}{}
	}

	errs = append(errs, validateExtraVolumeMounts(declaredNames, mounts, mountsPath)...)
	return errs
}

// validateExtraVolumeMounts checks extraVolumeMounts against the volume names declared in
// extraVolumes. declaredNames is nil when extraVolumes itself is unset or empty, so every
// mount then fails the "names a declared volume" check.
func validateExtraVolumeMounts(declaredNames map[string]struct{}, mounts []corev1.VolumeMount, mountsPath *field.Path) field.ErrorList {
	var errs field.ErrorList
	seenPaths := make(map[string]struct{}, len(mounts))

	for i, m := range mounts {
		idxPath := mountsPath.Index(i)

		if _, ok := declaredNames[m.Name]; !ok {
			errs = append(errs, field.Invalid(idxPath.Child("name"), m.Name,
				"does not match any entry in extraVolumes; mounting an operator-managed base "+
					"volume at a second path is not supported"))
		}

		switch {
		case !path.IsAbs(m.MountPath):
			errs = append(errs, field.Invalid(idxPath.Child("mountPath"), m.MountPath,
				"must be an absolute path"))
		case path.Clean(m.MountPath) != m.MountPath:
			errs = append(errs, field.Invalid(idxPath.Child("mountPath"), m.MountPath,
				fmt.Sprintf("must be a cleaned path; use %q", path.Clean(m.MountPath))))
		default:
			if resources.IsReservedMountPath(m.MountPath) {
				errs = append(errs, field.Invalid(idxPath.Child("mountPath"), m.MountPath,
					"is reserved for an operator-managed mount"))
			}
			if _, dup := seenPaths[m.MountPath]; dup {
				errs = append(errs, field.Duplicate(idxPath.Child("mountPath"), m.MountPath))
			}
		}
		seenPaths[m.MountPath] = struct{}{}
	}
	return errs
}
