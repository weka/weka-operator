package reporter

import (
	"bytes"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// kindTaggedLine is the envelope written per item in the NDJSON stream.
type kindTaggedLine struct {
	Kind   string          `json:"kind"`
	Object json.RawMessage `json:"object"`
}

// appendNDJSON strips managedFields and the last-applied-configuration annotation
// from each object, wraps it as {"kind":<kind>,"object":<stripped obj>}, and
// appends one NDJSON line to buf. It never mutates the cached originals.
func appendNDJSON(buf *bytes.Buffer, kind string, objs []client.Object) error {
	for i, obj := range objs {
		cp, ok := obj.DeepCopyObject().(client.Object)
		if !ok {
			return fmt.Errorf("object at index %d does not implement client.Object after DeepCopy", i)
		}
		cp.SetManagedFields(nil)

		ann := cp.GetAnnotations()
		if ann != nil {
			delete(ann, lastAppliedAnnotation)
			if len(ann) == 0 {
				ann = nil
			}
			cp.SetAnnotations(ann)
		}

		objBytes, err := json.Marshal(cp)
		if err != nil {
			return fmt.Errorf("marshal object at index %d (kind %s): %w", i, kind, err)
		}

		line := kindTaggedLine{Kind: kind, Object: json.RawMessage(objBytes)}
		lineBytes, err := json.Marshal(line)
		if err != nil {
			return fmt.Errorf("marshal kind-tagged line at index %d (kind %s): %w", i, kind, err)
		}
		buf.Write(lineBytes)
		buf.WriteByte('\n')
	}
	return nil
}

// appendNodeNDJSON appends pre-built node summary structs as kind-tagged NDJSON
// lines ("kind":"Node"). Each summary is already a plain struct (not a
// client.Object), so no strip step is needed.
func appendNodeNDJSON(buf *bytes.Buffer, summaries []nodeSummary) error {
	for i, s := range summaries {
		objBytes, err := json.Marshal(s)
		if err != nil {
			return fmt.Errorf("marshal node summary at index %d: %w", i, err)
		}
		line := kindTaggedLine{Kind: "Node", Object: json.RawMessage(objBytes)}
		lineBytes, err := json.Marshal(line)
		if err != nil {
			return fmt.Errorf("marshal node kind-tagged line at index %d: %w", i, err)
		}
		buf.Write(lineBytes)
		buf.WriteByte('\n')
	}
	return nil
}
