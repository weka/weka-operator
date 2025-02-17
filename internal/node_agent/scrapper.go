package node_agent

import (
	"bytes"
	"strings"

	"github.com/golang/protobuf/proto"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

// TransformMetrics takes original Prometheus metrics (in text format) as a []byte,
// applies extraLabels to every metric, and prepends a prefix (with dashes replaced by underscores)
// to every metric name. It preserves the original HELP and TYPE strings.
// It returns the transformed metrics as a single []byte.
func TransformMetrics(original []byte, extraLabels map[string]string, prefix string) ([]byte, error) {
	// Replace dashes in prefix with underscores.
	cleanPrefix := strings.ReplaceAll(prefix, "-", "_")

	// Parse the original metrics into a map of metric families.
	parser := expfmt.TextParser{}
	mfMap, err := parser.TextToMetricFamilies(bytes.NewReader(original))
	if err != nil {
		return nil, err
	}

	// Process each metric family.
	for _, mf := range mfMap {
		// Update the metric family name by prepending the clean prefix.
		if mf.Name != nil {
			origName := *mf.Name
			newName := cleanPrefix + "_" + origName
			mf.Name = proto.String(newName)
		}

		// For each metric in the family, add/update extra labels.
		for _, m := range mf.Metric {
			// Build a map of existing labels.
			labelMap := make(map[string]string)
			for _, lp := range m.Label {
				labelMap[lp.GetName()] = lp.GetValue()
			}
			// Add/override with extra labels.
			for k, v := range extraLabels {
				labelMap[k] = v
			}
			// Rebuild the label slice.
			newLabels := make([]*dto.LabelPair, 0, len(labelMap))
			for name, value := range labelMap {
				newLabels = append(newLabels, &dto.LabelPair{
					Name:  proto.String(name),
					Value: proto.String(value),
				})
			}
			m.Label = newLabels
		}
	}

	// Encode the modified metric families back into text format.
	var buf bytes.Buffer
	encoder := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfMap {
		if err := encoder.Encode(mf); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}
