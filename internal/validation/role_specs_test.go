package validation

import (
	"reflect"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

// fieldByJSONTag finds the struct field whose json tag name matches tag.
func fieldByJSONTag(v reflect.Value, tag string) (reflect.Value, bool) {
	t := v.Type()
	for i := range t.NumField() {
		if strings.Split(t.Field(i).Tag.Get("json"), ",")[0] == tag {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// TestRolesForTemplateFieldWiring is the guard that lets four validators share one role table: it
// checks every *Field name is a real WekaClusterTemplate json field AND that each slot reads that
// field's value. Every field gets a distinct value, so a copy-paste that crosses roles (nfsCores
// read into the smbw row) or kinds (containers read into cores) shows up as a mismatch rather than
// a coincidence. The names reach users in admission messages, so a rename that drifts here is a
// silently wrong error message, not a compile failure.
func TestRolesForTemplateFieldWiring(t *testing.T) {
	template := &weka.WekaClusterTemplate{}
	rv := reflect.ValueOf(template).Elem()

	want := map[string]int{}
	next := 0
	for _, r := range rolesForTemplate(&weka.WekaClusterTemplate{}) {
		for _, tag := range []string{r.coresField, r.hugepagesField, r.containersField} {
			f, ok := fieldByJSONTag(rv, tag)
			if !ok {
				t.Fatalf("role %q names spec field %q, which is not a json field on WekaClusterTemplate", r.role, tag)
			}
			if f.Kind() != reflect.Int {
				t.Fatalf("spec field %q is %s, want int", tag, f.Kind())
			}
			if _, dup := want[tag]; dup {
				t.Fatalf("spec field %q is named by more than one role/kind slot", tag)
			}
			next++
			f.SetInt(int64(next))
			want[tag] = next
		}
	}

	for _, r := range rolesForTemplate(template) {
		slots := []struct {
			kind string
			tag  string
			got  int
		}{
			{"cores", r.coresField, r.cores},
			{"hugepages", r.hugepagesField, r.hugepages},
			{"containers", r.containersField, r.containers},
		}
		for _, s := range slots {
			if s.got != want[s.tag] {
				t.Errorf("role %q %s: got %d from %q, want %d — values are crossed between fields or roles",
					r.role, s.kind, s.got, s.tag, want[s.tag])
			}
		}
	}
}

// TestRolesForTemplateCoversEveryRole checks the table's order and membership against a
// hand-written list, and separately guards against a role silently added to the API: it anchors
// on RoleNodeSelector, the struct that grows when a role becomes selector-addressable, and whose
// field count therefore tracks the number of per-role validators that must exist. RoleNodeSelector
// has no Envoy field — envoy is not selector-addressable, so it is correctly excluded here too.
func TestRolesForTemplateCoversEveryRole(t *testing.T) {
	want := []string{
		weka.WekaContainerModeDrive,
		weka.WekaContainerModeCompute,
		weka.WekaContainerModeS3,
		weka.WekaContainerModeNfs,
		weka.WekaContainerModeSmbw,
		weka.WekaContainerModeDataServices,
	}
	got := make([]string, 0, len(want))
	roles := map[string]bool{}
	for _, r := range rolesForTemplate(&weka.WekaClusterTemplate{}) {
		got = append(got, r.role)
		roles[r.role] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("roles = %v, want %v", got, want)
	}

	selType := reflect.TypeOf(weka.RoleNodeSelector{})
	if selType.NumField() != len(roles) {
		t.Errorf("RoleNodeSelector has %d fields but rolesForTemplate has %d roles — a role was added "+
			"to the API and is now skipped by every per-role validator", selType.NumField(), len(roles))
	}
	for i := range selType.NumField() {
		sf := selType.Field(i)
		// dataServices is the one role whose json name and mode string differ.
		tag := strings.Split(sf.Tag.Get("json"), ",")[0]
		if tag == "dataServices" {
			tag = weka.WekaContainerModeDataServices
		}
		if !roles[tag] {
			t.Errorf("RoleNodeSelector.%s (json %q) has no row in rolesForTemplate — a role was added "+
				"to the API and is now skipped by every per-role validator", sf.Name, tag)
		}
	}
}

// TestRolesForTemplateRolesAreSelectorAddressable catches a mistyped role string. GetNodeSelectorForRole
// falls back to spec.nodeSelector for a role it does not recognize, so a typo would not fail — it would
// quietly validate against the wrong node set.
func TestRolesForTemplateRolesAreSelectorAddressable(t *testing.T) {
	cluster := &weka.WekaCluster{}
	cluster.Spec.NodeSelector = map[string]string{"which": "fallback"}
	perRole := map[string]*map[string]string{}
	for _, r := range rolesForTemplate(&weka.WekaClusterTemplate{}) {
		sel := map[string]string{"which": r.role}
		perRole[r.role] = &sel
	}
	cluster.Spec.RoleNodeSelector = weka.RoleNodeSelector{
		Drive:        perRole[weka.WekaContainerModeDrive],
		Compute:      perRole[weka.WekaContainerModeCompute],
		S3:           perRole[weka.WekaContainerModeS3],
		Nfs:          perRole[weka.WekaContainerModeNfs],
		Smbw:         perRole[weka.WekaContainerModeSmbw],
		DataServices: perRole[weka.WekaContainerModeDataServices],
	}

	for _, r := range rolesForTemplate(&weka.WekaClusterTemplate{}) {
		got := cluster.GetNodeSelectorForRole(r.role)["which"]
		if got != r.role {
			t.Errorf("GetNodeSelectorForRole(%q) resolved to %q — the role string is not one the API recognizes",
				r.role, got)
		}
	}
}

// TestRolesForTemplateNil documents the nil-safety the validators rely on: a nil template is
// auto-full-drives mode, and a panic in a webhook rejects every apply through failurePolicy: Fail.
func TestRolesForTemplateNil(t *testing.T) {
	if got := rolesForTemplate(nil); got != nil {
		t.Errorf("rolesForTemplate(nil) = %v, want nil", got)
	}
}
