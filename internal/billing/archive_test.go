package billing

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLatestSettledSnapshot(t *testing.T) {
	base := fixtureDocument(t)
	older := base
	older.TimeFrameStart -= int64(time.Hour / time.Millisecond)
	older.TimeFrameEnd -= int64(time.Hour / time.Millisecond)
	future := base
	future.TimeFrameStart += int64(time.Hour / time.Millisecond)
	future.TimeFrameEnd += int64(time.Hour / time.Millisecond)
	archive := makeArchive(t, []Document{future, older, base})

	cutoff := time.UnixMilli(base.TimeFrameEnd).Add(30 * time.Minute)
	snapshot, err := LatestSettledSnapshot(archive, cutoff, map[string]string{
		"11111111-1111-1111-1111-111111111111": "Example",
	}, DefaultParseLimits())
	if err != nil {
		t.Fatalf("LatestSettledSnapshot: %v", err)
	}
	if !snapshot.PeriodEnd.Equal(time.UnixMilli(base.TimeFrameEnd)) {
		t.Fatalf("period end = %s, want %s", snapshot.PeriodEnd, time.UnixMilli(base.TimeFrameEnd))
	}
	if len(snapshot.Environments) != 1 || snapshot.Environments[0].Name != "Example" {
		t.Fatalf("unexpected environments: %#v", snapshot.Environments)
	}
	env := snapshot.Environments[0]
	if got := env.HostUnitsByMode["full_stack"]; got != 0.5 {
		t.Fatalf("full-stack units = %v, want 0.5", got)
	}
	if got := env.HostUnitsByMode["infrastructure"]; got != 0.6 {
		t.Fatalf("infrastructure units = %v, want 0.6", got)
	}
	if got := env.DEMBySource["synthetic"]; got != 4.5 {
		t.Fatalf("synthetic DEM = %v, want 4.5", got)
	}
}

func TestLatestSettledSnapshotErrors(t *testing.T) {
	base := fixtureDocument(t)
	archive := makeArchive(t, []Document{base})
	if _, err := LatestSettledSnapshot(archive, time.UnixMilli(base.TimeFrameStart), nil, DefaultParseLimits()); err == nil || !strings.Contains(err.Error(), "no billing interval") {
		t.Fatalf("no-settled error = %v", err)
	}
	if _, err := LatestSettledSnapshot([]byte("not a zip"), time.Now(), nil, DefaultParseLimits()); err == nil {
		t.Fatal("malformed ZIP unexpectedly succeeded")
	}
	limits := DefaultParseLimits()
	limits.MaxJSONDocumentBytes = 10
	if _, err := LatestSettledSnapshot(archive, time.Now(), nil, limits); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("JSON size error = %v", err)
	}
	limits = DefaultParseLimits()
	limits.MaxDocuments = 1
	if _, err := LatestSettledSnapshot(makeArchive(t, []Document{base, base}), time.Now(), nil, limits); err == nil || !strings.Contains(err.Error(), "documents") {
		t.Fatalf("document count error = %v", err)
	}
}

func fixtureDocument(t *testing.T) Document {
	t.Helper()
	raw, err := os.ReadFile("testdata/billing-record.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func makeArchive(t *testing.T, docs []Document) []byte {
	t.Helper()
	var nested bytes.Buffer
	nestedWriter := zip.NewWriter(&nested)
	for i, doc := range docs {
		entry, err := nestedWriter.Create(time.UnixMilli(doc.TimeFrameStart).Format("20060102T150405") + "_" + string(rune('a'+i)) + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(entry).Encode(doc); err != nil {
			t.Fatal(err)
		}
	}
	if err := nestedWriter.Close(); err != nil {
		t.Fatal(err)
	}

	var outer bytes.Buffer
	outerWriter := zip.NewWriter(&outer)
	entry, err := outerWriter.Create("billingRecords_example.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(nested.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := outerWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return outer.Bytes()
}
