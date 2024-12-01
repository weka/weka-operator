package metrics

import "sync"

type PromResponse struct {
	lock    sync.Mutex
	Metrics map[string]*PromMetric
}

func NewPromResponse() *PromResponse {
	return &PromResponse{
		Metrics: map[string]*PromMetric{},
	}
}

func (p *PromResponse) AddMetric(metric PromMetric, taggedValues []TaggedValue) {
	p.lock.Lock()
	defer p.lock.Unlock()

	if _, ok := p.Metrics[metric.Metric]; !ok {
		p.Metrics[metric.Metric] = &metric
	}

	p.Metrics[metric.Metric].ValuesByTags = append(p.Metrics[metric.Metric].ValuesByTags, taggedValues...)
}

func (p *PromResponse) String() string {
	p.lock.Lock()
	defer p.lock.Unlock()

	ret := ""
	for _, metric := range p.Metrics {
		ret = ret + *metric.AsPrometheusString(nil) + "\n"
	}

	return ret
}
