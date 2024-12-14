package metrics

import (
	"fmt"
	"github.com/weka/weka-operator/pkg/util"
	"strings"
	"time"
)

type TagMap map[string]string

type TaggedValue struct {
	Tags      map[string]string
	Value     float64
	Timestamp time.Time
}

type PromMetric struct {
	Metric       string
	Value        *float64
	ValuesByTags []TaggedValue
	Timestamp    time.Time
	Tags         map[string]string
	Help         string
	Type         string
}

func NewTaggedValue(tags map[string]string, value float64) TaggedValue {
	return TaggedValue{
		Tags:  tags,
		Value: value,
	}
}

func (m PromMetric) AsPrometheusString(defaultTags map[string]string) *string {
	//TODO: non optimal implementation, but this is also not a data path
	tags := []string{}

	for k, v := range util.MapOrdered(defaultTags) {
		tags = append(tags, fmt.Sprintf("%s=\"%s\"", k, v))
	}

	for k, v := range util.MapOrdered(m.Tags) {
		label := NormalizeLabelName(k)
		tags = append(tags, fmt.Sprintf("%s=\"%s\"", label, v))
	}

	mtype := m.Type
	if mtype == "" {
		mtype = "gauge"
	}

	ret := ""
	ret = ret + "# HELP " + m.Metric + " " + m.Help + "\n"
	ret = ret + "# TYPE " + m.Metric + " " + mtype + "\n"

	ts := time.Now()
	if !m.Timestamp.IsZero() {
		ts = m.Timestamp
	}

	if len(m.ValuesByTags) == 0 && m.Value != nil {
		ret = ret + fmt.Sprintf("%s{%s} %f %d", m.Metric, strings.Join(tags, ","), *m.Value, ts.UnixMilli())
		return &ret
	} else if len(m.ValuesByTags) == 0 {
		return nil // no data
	}

	parts := []string{}

	for _, perm := range m.ValuesByTags {
		//copy tags into new list
		permTags := append([]string{}, tags...)
		for k, v := range util.MapOrdered(perm.Tags) {
			permTags = append(permTags, fmt.Sprintf("%s=\"%s\"", k, v))
		}
		tagTs := ts
		if !perm.Timestamp.IsZero() {
			tagTs = perm.Timestamp
		}

		parts = append(parts, fmt.Sprintf("%s{%s} %f %d", m.Metric, strings.Join(permTags, ","), perm.Value, tagTs.UnixMilli()))
	}
	ret = ret + strings.Join(parts, "\n")

	return &ret
}

// NormalizeLabelName replaces all invalid characters in a label name with underscores
func NormalizeLabelName(str string) string {
	str = strings.ReplaceAll(str, "/", "_")
	str = strings.ReplaceAll(str, "-", "_")
	str = strings.ReplaceAll(str, ".", "_")
	return str
}
