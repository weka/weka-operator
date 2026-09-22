package v1alpha1

import (
	"fmt"
	"sort"
	"strings"
)

// GetType returns the effective type of the policy. See WekaPolicySpec.GetType.
func (p *WekaPolicy) GetType() (WekaPolicyType, bool, error) {
	return p.Spec.GetType()
}

// IsConfiguration reports whether the policy carries operator-wide settings rather than an
// operation to run.
func (p *WekaPolicy) IsConfiguration() bool {
	return p.Spec.Payload.Configuration != nil
}

// GetType returns the effective type of the policy, and whether it is a configuration policy.
//
// An explicit spec.type always wins, so policies written before the field became optional keep
// dispatching exactly as they did. Otherwise the type is derived from whichever payload is
// populated. Anything other than a single payload is an error rather than a guess: no payload
// leaves nothing to derive from, and several leave no basis to choose between them. Either way
// the caller can disambiguate by setting spec.type.
//
// A configuration policy runs nothing, so it has no WekaPolicyType of its own and is reported
// through isConfiguration instead. Combining it with spec.type or with an operation payload is
// rejected, since the operation would otherwise be silently dropped.
func (s *WekaPolicySpec) GetType() (typ WekaPolicyType, isConfiguration bool, err error) {
	fields, types := s.Payload.runnablePayloads()

	if s.Payload.Configuration != nil {
		if s.Type != "" {
			return "", false, fmt.Errorf("configurationPayload cannot be combined with spec.type %q: a configuration policy runs nothing, so the operation would be dropped", s.Type)
		}
		if len(fields) > 0 {
			return "", false, fmt.Errorf("configurationPayload cannot be combined with %s: a configuration policy runs nothing, so the operation would never run", strings.Join(sorted(fields), ", "))
		}
		return "", true, nil
	}

	if s.Type != "" {
		return s.Type, false, nil
	}

	switch len(types) {
	case 1:
		return types[0], false, nil
	case 0:
		return "", false, fmt.Errorf("cannot determine policy type: spec.type is unset and no recognized payload is set; set spec.type or provide one of %s", strings.Join(allPayloadFields(), ", "))
	default:
		return "", false, fmt.Errorf("cannot determine policy type: spec.type is unset and %d payloads are set (%s); provide exactly one payload", len(types), strings.Join(sorted(fields), ", "))
	}
}

// runnablePayloads reports which payloads are populated that map to an operation the policy
// controller runs, and the types they imply.
//
// SchedulingConfig is absent: it has no corresponding WekaPolicyType, so it can neither be
// derived from nor dispatched to. Configuration is absent because it runs nothing.
func (p *PolicyPayload) runnablePayloads() (fields []string, types []WekaPolicyType) {
	if p.SignDrives != nil {
		fields, types = append(fields, "signDrivesPayload"), append(types, WekaPolicyTypeSignDrives)
	}
	if p.DiscoverDrives != nil {
		fields, types = append(fields, "discoverDrivesPayload"), append(types, WekaPolicyTypeDiscoverDrives)
	}
	if p.EnsureNICs != nil {
		fields, types = append(fields, "ensureNICsPayload"), append(types, WekaPolicyTypeEnsureNICs)
	}
	if p.DriverDistPayload != nil {
		fields, types = append(fields, "driverDistPayload"), append(types, WekaPolicyTypeEnableLocalDriversDistribution)
	}
	if p.RemoteTracesSession != nil {
		fields, types = append(fields, "remoteTracesSessionPayload"), append(types, WekaPolicyTypeRemoteTracesSession)
	}
	if p.CleanStaleVirtualDrives != nil {
		fields, types = append(fields, "cleanStaleVirtualDrivesPayload"), append(types, WekaPolicyTypeCleanStaleVirtualDrives)
	}
	return fields, types
}

func allPayloadFields() []string {
	return []string{
		"signDrivesPayload",
		"discoverDrivesPayload",
		"ensureNICsPayload",
		"driverDistPayload",
		"remoteTracesSessionPayload",
		"cleanStaleVirtualDrivesPayload",
		"configurationPayload",
	}
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
