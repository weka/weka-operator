package reporter

import (
	"context"
	"sort"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// eventSource identifies the component that reported an event.
type eventSource struct {
	Component string `json:"component,omitempty"`
	Host      string `json:"host,omitempty"`
}

// eventSummary is the describe-like projection of a core/v1 Event, embedded
// under the synthetic _events key of the involved object's snapshot JSON.
// Both timestamp families ship raw: events written via the events.k8s.io API
// leave first/lastTimestamp empty and carry eventTime instead.
type eventSummary struct {
	Type           string            `json:"type,omitempty"`
	Reason         string            `json:"reason,omitempty"`
	Message        string            `json:"message,omitempty"`
	Count          int32             `json:"count,omitempty"`
	FirstTimestamp *metav1.Time      `json:"firstTimestamp,omitempty"`
	LastTimestamp  *metav1.Time      `json:"lastTimestamp,omitempty"`
	EventTime      *metav1.MicroTime `json:"eventTime,omitempty"`
	Source         *eventSource      `json:"source,omitempty"`
}

// eventIndex maps reported objects to their raw event buckets. A nil index is
// valid and empty — the events List failed that cycle and enrichment is
// skipped. Projection happens lazily at lookup (forUID/forNode), since each
// key is looked up at most once per cycle — memoizing would just add bookkeeping.
type eventIndex struct {
	byUID  map[types.UID][]*corev1.Event
	byNode map[string][]*corev1.Event
}

func (ix *eventIndex) forUID(uid types.UID) []eventSummary {
	if ix == nil {
		return nil
	}
	return projectSorted(ix.byUID[uid])
}

func (ix *eventIndex) forNode(name string) []eventSummary {
	if ix == nil {
		return nil
	}
	return projectSorted(ix.byNode[name])
}

// eventsListTimeout bounds the uncached events List — the only direct
// API-server call in a cycle; unbounded, a hung List would stall every
// subsequent cycle, not just skip this one.
const eventsListTimeout = time.Minute

// collectEventIndex lists all cluster events and buckets them by involved
// object. The uncached reader is deliberate: the cached client would lazily
// start a permanent cluster-wide Event informer for a once-per-interval read.
func collectEventIndex(ctx context.Context, reader client.Reader, log logr.Logger) *eventIndex {
	ctx, cancel := context.WithTimeout(ctx, eventsListTimeout)
	defer cancel()
	list := &corev1.EventList{}
	if err := reader.List(ctx, list); err != nil {
		log.Error(err, "listing events failed, snapshot will be sent without events this cycle")
		return nil
	}
	return buildEventIndex(list.Items)
}

// buildEventIndex buckets raw events by involved object only — projecting
// every bucket eagerly would waste CPU on events for objects never reported.
func buildEventIndex(events []corev1.Event) *eventIndex {
	ix := &eventIndex{
		byUID:  map[types.UID][]*corev1.Event{},
		byNode: map[string][]*corev1.Event{},
	}
	for i := range events {
		ev := &events[i]
		ref := ev.InvolvedObject
		// Core Node events are matched by name — the Node projection carries no
		// uid, and kubelet often sets involvedObject.uid to the node NAME anyway.
		// The apiVersion guard keeps same-named CRD kinds (e.g. longhorn.io Node)
		// out of the core Node's bucket.
		if ref.Kind == "Node" && ref.Name != "" && (ref.APIVersion == "" || ref.APIVersion == "v1") {
			ix.byNode[ref.Name] = append(ix.byNode[ref.Name], ev)
			continue
		}
		if ref.UID != "" {
			ix.byUID[ref.UID] = append(ix.byUID[ref.UID], ev)
		}
	}
	return ix
}

// projectSorted projects a bucket of events sorted by last-seen, ascending
// (describe order: most recent last).
func projectSorted(evs []*corev1.Event) []eventSummary {
	if len(evs) == 0 {
		return nil
	}
	sort.SliceStable(evs, func(i, j int) bool {
		return lastSeen(evs[i]).Before(lastSeen(evs[j]))
	})
	out := make([]eventSummary, len(evs))
	for i, ev := range evs {
		out[i] = projectEvent(ev)
	}
	return out
}

// lastSeen returns time.Time (not metav1.Time): metav1.Time's Before is a
// pointer-receiver method, uncallable on a non-addressable function return.
func lastSeen(ev *corev1.Event) time.Time {
	if ev.Series != nil && !ev.Series.LastObservedTime.IsZero() {
		return ev.Series.LastObservedTime.Time
	}
	if !ev.LastTimestamp.IsZero() {
		return ev.LastTimestamp.Time
	}
	if !ev.EventTime.IsZero() {
		return ev.EventTime.Time
	}
	return ev.FirstTimestamp.Time
}

func projectEvent(ev *corev1.Event) eventSummary {
	s := eventSummary{
		Type:    ev.Type,
		Reason:  ev.Reason,
		Message: ev.Message,
		Count:   ev.Count,
	}
	// events.k8s.io-written repeating events carry count/recency in Series
	// (describe consults it first); normalize into the same summary fields.
	if ev.Count == 0 && ev.Series != nil {
		s.Count = ev.Series.Count
	}
	if !ev.FirstTimestamp.IsZero() {
		t := ev.FirstTimestamp
		s.FirstTimestamp = &t
	}
	if !ev.LastTimestamp.IsZero() {
		t := ev.LastTimestamp
		s.LastTimestamp = &t
	} else if ev.Series != nil && !ev.Series.LastObservedTime.IsZero() {
		t := metav1.Time{Time: ev.Series.LastObservedTime.Time}
		s.LastTimestamp = &t
	}
	if !ev.EventTime.IsZero() {
		t := ev.EventTime
		s.EventTime = &t
	}
	// describe's source join: legacy events carry Source; events.k8s.io-written
	// ones carry reportingComponent/reportingInstance instead.
	component, host := ev.Source.Component, ev.Source.Host
	if component == "" {
		component = ev.ReportingController
	}
	if host == "" {
		host = ev.ReportingInstance
	}
	if component != "" || host != "" {
		s.Source = &eventSource{Component: component, Host: host}
	}
	return s
}
