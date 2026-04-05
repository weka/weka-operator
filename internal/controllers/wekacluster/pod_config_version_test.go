package wekacluster

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/config"
)

func TestCalcClusterPodConfigVersion_ToggleWekaRuntimeVersionRotation(t *testing.T) {
	config.Config.PodConfigVersion = "1"
	spec := &weka.WekaClusterSpec{Image: "quay.io/weka.io/weka-in-container:4.5.0"}

	config.Config.EnablePodConfigCodeVersionRotation = false
	hashWithout := CalcClusterPodConfigVersion(spec)

	config.Config.EnablePodConfigCodeVersionRotation = true
	hashWith := CalcClusterPodConfigVersion(spec)

	if hashWithout == hashWith {
		t.Errorf("enabling EnablePodConfigCodeVersionRotation should change the hash, got %s == %s", hashWithout, hashWith)
	}
}

func TestCalcClusterPodConfigVersion_StableWhenDisabled(t *testing.T) {
	config.Config.PodConfigVersion = "1"
	config.Config.EnablePodConfigCodeVersionRotation = false
	spec := &weka.WekaClusterSpec{Image: "quay.io/weka.io/weka-in-container:4.5.0"}

	hash1 := CalcClusterPodConfigVersion(spec)
	hash2 := CalcClusterPodConfigVersion(spec)

	if hash1 != hash2 {
		t.Errorf("hash should be stable across calls, got %s != %s", hash1, hash2)
	}
}

func TestCalcClusterPodConfigVersion_ChangesWhenImageChanges(t *testing.T) {
	config.Config.PodConfigVersion = "1"
	config.Config.EnablePodConfigCodeVersionRotation = false

	spec1 := &weka.WekaClusterSpec{Image: "quay.io/weka.io/weka-in-container:4.5.0"}
	spec2 := &weka.WekaClusterSpec{Image: "quay.io/weka.io/weka-in-container:4.6.0"}

	if CalcClusterPodConfigVersion(spec1) == CalcClusterPodConfigVersion(spec2) {
		t.Error("hash should differ when image changes")
	}
}

func TestCalcClusterPodConfigVersion_ChangesWhenPodConfigVersionChanges(t *testing.T) {
	config.Config.EnablePodConfigCodeVersionRotation = false
	spec := &weka.WekaClusterSpec{Image: "quay.io/weka.io/weka-in-container:4.5.0"}

	config.Config.PodConfigVersion = "1"
	hash1 := CalcClusterPodConfigVersion(spec)

	config.Config.PodConfigVersion = "2"
	hash2 := CalcClusterPodConfigVersion(spec)

	if hash1 == hash2 {
		t.Error("hash should differ when PodConfigVersion changes")
	}
}
