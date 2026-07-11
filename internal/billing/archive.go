package billing

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ParseLimits bounds all attacker- or server-controlled archive expansion.
type ParseLimits struct {
	MaxNestedArchiveBytes int64
	MaxJSONDocumentBytes  int64
	MaxDocuments          int
}

// DefaultParseLimits are deliberately larger than a normal short billing window.
func DefaultParseLimits() ParseLimits {
	return ParseLimits{
		MaxNestedArchiveBytes: 128 << 20,
		MaxJSONDocumentBytes:  8 << 20,
		MaxDocuments:          1000,
	}
}

// LatestSettledSnapshot parses a nested billing ZIP and selects the newest
// interval whose end is not later than cutoff.
func LatestSettledSnapshot(outer []byte, cutoff time.Time, environmentNames map[string]string, limits ParseLimits) (*Snapshot, error) {
	limits = normalizeLimits(limits)
	outerZip, err := zip.NewReader(bytes.NewReader(outer), int64(len(outer)))
	if err != nil {
		return nil, fmt.Errorf("open outer billing archive: %w", err)
	}
	nestedFile, err := findNestedArchive(outerZip.File)
	if err != nil {
		return nil, err
	}
	nested, err := readZipFile(nestedFile, limits.MaxNestedArchiveBytes)
	if err != nil {
		return nil, fmt.Errorf("read nested billing archive: %w", err)
	}
	nestedZip, err := zip.NewReader(bytes.NewReader(nested), int64(len(nested)))
	if err != nil {
		return nil, fmt.Errorf("open nested billing archive: %w", err)
	}

	jsonFiles := make([]*zip.File, 0, len(nestedZip.File))
	for _, file := range nestedZip.File {
		if strings.EqualFold(filepath.Ext(file.Name), ".json") {
			jsonFiles = append(jsonFiles, file)
		}
	}
	if len(jsonFiles) == 0 {
		return nil, fmt.Errorf("nested billing archive contains no JSON documents")
	}
	if len(jsonFiles) > limits.MaxDocuments {
		return nil, fmt.Errorf("nested billing archive has %d documents, limit is %d", len(jsonFiles), limits.MaxDocuments)
	}

	var latest *Document
	cutoffMillis := cutoff.UnixMilli()
	for _, file := range jsonFiles {
		raw, err := readZipFile(file, limits.MaxJSONDocumentBytes)
		if err != nil {
			return nil, fmt.Errorf("read billing document %q: %w", file.Name, err)
		}
		var doc Document
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("decode billing document %q: %w", file.Name, err)
		}
		if doc.TimeFrameEnd <= cutoffMillis && (latest == nil || doc.TimeFrameEnd > latest.TimeFrameEnd) {
			copy := doc
			latest = &copy
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("archive contains no billing interval settled before %s", cutoff.UTC().Format(time.RFC3339))
	}
	return CalculateSnapshot(*latest, environmentNames)
}

func normalizeLimits(limits ParseLimits) ParseLimits {
	defaults := DefaultParseLimits()
	if limits.MaxNestedArchiveBytes <= 0 {
		limits.MaxNestedArchiveBytes = defaults.MaxNestedArchiveBytes
	}
	if limits.MaxJSONDocumentBytes <= 0 {
		limits.MaxJSONDocumentBytes = defaults.MaxJSONDocumentBytes
	}
	if limits.MaxDocuments <= 0 {
		limits.MaxDocuments = defaults.MaxDocuments
	}
	return limits
}

func findNestedArchive(files []*zip.File) (*zip.File, error) {
	candidates := make([]*zip.File, 0, 1)
	for _, file := range files {
		base := filepath.Base(file.Name)
		if strings.HasPrefix(base, "billingRecords_") && strings.EqualFold(filepath.Ext(base), ".zip") {
			candidates = append(candidates, file)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("outer billing archive contains no billingRecords_*.zip")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	return candidates[len(candidates)-1], nil
}

func readZipFile(file *zip.File, maxBytes int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(maxBytes) {
		return nil, fmt.Errorf("uncompressed size %d exceeds limit %d", file.UncompressedSize64, maxBytes)
	}
	r, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	limited := io.LimitReader(r, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("expanded content exceeds limit %d", maxBytes)
	}
	return data, nil
}
