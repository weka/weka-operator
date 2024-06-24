package main

import (
	"testing"
)

func TestTags(t *testing.T) {
	r, err := NewRepository()
	if err != nil {
		t.Fatal(err)
	}

	tagMap, err := r.tags()
	if err != nil {
		t.Fatal(err)
	}

	if len(tagMap) == 0 {
		t.Fatal("no tags found")
	}

	latestTag, err := r.MostRecentTag()
	if err != nil {
		t.Fatal(err)
	}

	if latestTag == "" {
		t.Fatal("no tag found history of HEAD")
	}
	if latestTag != "v1.0.0-beta.6" {
		t.Errorf("unexpected tag: %s", latestTag)
	}
}
