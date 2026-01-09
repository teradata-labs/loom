// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package artifacts

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// AnalysisResult contains the results of file analysis.
type AnalysisResult struct {
	ContentType string
	SizeBytes   int64
	Checksum    string
	Tags        []string
	Metadata    map[string]string
}

// Analyzer analyzes files and extracts metadata.
type Analyzer struct{}

// NewAnalyzer creates a new file analyzer.
func NewAnalyzer() *Analyzer {
	return &Analyzer{}
}

// Analyze inspects a file and returns analysis results.
func (a *Analyzer) Analyze(path string) (*AnalysisResult, error) {
	// Get file info
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	// Compute checksum
	checksum, err := ComputeChecksum(path)
	if err != nil {
		return nil, fmt.Errorf("failed to compute checksum: %w", err)
	}

	// Detect content type
	contentType, err := a.detectContentType(path)
	if err != nil {
		return nil, fmt.Errorf("failed to detect content type: %w", err)
	}

	// Infer tags
	tags := a.inferTags(filepath.Base(path), contentType)

	// Extract format-specific metadata
	metadata := a.extractMetadata(path, contentType)

	return &AnalysisResult{
		ContentType: contentType,
		SizeBytes:   info.Size(),
		Checksum:    checksum,
		Tags:        tags,
		Metadata:    metadata,
	}, nil
}

// detectContentType detects the MIME type of a file using multiple strategies.
func (a *Analyzer) detectContentType(path string) (string, error) {
	// Strategy 1: Try file extension first (fast)
	ext := strings.ToLower(filepath.Ext(path))
	if contentType := mime.TypeByExtension(ext); contentType != "" {
		return contentType, nil
	}

	// Strategy 2: Read file header for magic bytes
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	// Read first 512 bytes for content detection
	buffer := make([]byte, 512)
	n, err := f.Read(buffer)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	// Detect content type from buffer
	contentType := http.DetectContentType(buffer[:n])

	// If still unknown, try common extensions
	if contentType == "application/octet-stream" || contentType == "text/plain" {
		contentType = a.guessFromExtension(ext)
	}

	return contentType, nil
}

// guessFromExtension guesses content type from file extension for cases
// where standard MIME detection fails.
func (a *Analyzer) guessFromExtension(ext string) string {
	switch ext {
	case ".yaml", ".yml":
		return "application/x-yaml"
	case ".toml":
		return "application/toml"
	case ".sql":
		return "application/sql"
	case ".md", ".markdown":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".doc":
		return "application/msword"
	case ".pdf":
		return "application/pdf"
	case ".zip":
		return "application/zip"
	case ".tar":
		return "application/x-tar"
	case ".gz":
		return "application/gzip"
	case ".py":
		return "text/x-python"
	case ".go":
		return "text/x-go"
	case ".js", ".mjs":
		return "application/javascript"
	case ".ts":
		return "application/typescript"
	case ".proto":
		return "text/x-protobuf"
	default:
		return "application/octet-stream"
	}
}

// inferTags generates automatic tags based on file name and content type.
func (a *Analyzer) inferTags(filename, contentType string) []string {
	tags := []string{}
	nameLower := strings.ToLower(filename)

	// Tag by content type category
	switch {
	case strings.Contains(contentType, "spreadsheet"), strings.Contains(contentType, "excel"):
		tags = append(tags, "excel", "spreadsheet", "data")
	case strings.Contains(contentType, "csv"):
		tags = append(tags, "csv", "data", "tabular")
	case strings.Contains(contentType, "json"):
		tags = append(tags, "json", "structured", "data")
	case strings.Contains(contentType, "yaml"), contentType == "application/x-yaml":
		tags = append(tags, "yaml", "config", "structured")
	case strings.Contains(contentType, "sql"):
		tags = append(tags, "sql", "database", "query")
	case strings.Contains(contentType, "markdown"):
		tags = append(tags, "markdown", "documentation", "text")
	case strings.Contains(contentType, "image"):
		tags = append(tags, "image", "media")
	case strings.Contains(contentType, "pdf"):
		tags = append(tags, "pdf", "document")
	case strings.Contains(contentType, "word"), strings.Contains(contentType, "msword"):
		tags = append(tags, "word", "document", "text")
	case strings.Contains(contentType, "python"):
		tags = append(tags, "python", "code", "script")
	case strings.Contains(contentType, "go"):
		tags = append(tags, "go", "code")
	case strings.Contains(contentType, "javascript"):
		tags = append(tags, "javascript", "code")
	case strings.Contains(contentType, "text"):
		tags = append(tags, "text")
	case strings.Contains(contentType, "gzip"), strings.Contains(contentType, "x-gzip"):
		tags = append(tags, "archive", "compressed")
	case strings.Contains(contentType, "zip"):
		tags = append(tags, "archive", "zip")
	case strings.Contains(contentType, "tar"), strings.Contains(contentType, "x-tar"):
		tags = append(tags, "archive", "tar")
	}

	// Tag by filename patterns
	if strings.Contains(nameLower, "report") {
		tags = append(tags, "report")
	}
	if strings.Contains(nameLower, "data") {
		tags = append(tags, "data")
	}
	if strings.Contains(nameLower, "config") {
		tags = append(tags, "config")
	}
	if strings.Contains(nameLower, "test") {
		tags = append(tags, "test")
	}
	if strings.Contains(nameLower, "doc") || strings.Contains(nameLower, "readme") {
		tags = append(tags, "documentation")
	}
	if strings.Contains(nameLower, "schema") {
		tags = append(tags, "schema")
	}
	if strings.Contains(nameLower, "template") {
		tags = append(tags, "template")
	}
	if strings.Contains(nameLower, "example") {
		tags = append(tags, "example")
	}

	// Deduplicate tags
	return deduplicateTags(tags)
}

// extractMetadata extracts format-specific metadata from files.
func (a *Analyzer) extractMetadata(path, contentType string) map[string]string {
	metadata := make(map[string]string)

	switch {
	case strings.Contains(contentType, "csv"):
		a.extractCSVMetadata(path, metadata)
	case strings.Contains(contentType, "json"):
		a.extractJSONMetadata(path, metadata)
		// Future: Add Excel, YAML, etc. metadata extraction
	}

	return metadata
}

// extractCSVMetadata extracts metadata from CSV files.
func (a *Analyzer) extractCSVMetadata(path string, metadata map[string]string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	reader := csv.NewReader(f)

	// Read first row (headers)
	headers, err := reader.Read()
	if err != nil {
		return
	}

	metadata["column_count"] = fmt.Sprintf("%d", len(headers))
	metadata["columns"] = strings.Join(headers, ", ")

	// Count rows (sample first 100)
	rowCount := 1 // Already read header
	for i := 0; i < 99; i++ {
		if _, err := reader.Read(); err != nil {
			if err == io.EOF {
				break
			}
			return
		}
		rowCount++
	}

	if rowCount >= 100 {
		metadata["rows"] = "100+ (sampled)"
	} else {
		metadata["rows"] = fmt.Sprintf("%d", rowCount)
	}
}

// extractJSONMetadata extracts metadata from JSON files.
func (a *Analyzer) extractJSONMetadata(path string, metadata map[string]string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	// Read and parse JSON
	var data interface{}
	decoder := json.NewDecoder(f)
	if err := decoder.Decode(&data); err != nil {
		metadata["valid_json"] = "false"
		metadata["error"] = "invalid JSON"
		return
	}

	metadata["valid_json"] = "true"

	// Detect structure type
	switch v := data.(type) {
	case map[string]interface{}:
		metadata["structure"] = "object"
		metadata["key_count"] = fmt.Sprintf("%d", len(v))
		// List first few keys
		keys := make([]string, 0, 5)
		for k := range v {
			keys = append(keys, k)
			if len(keys) >= 5 {
				break
			}
		}
		metadata["sample_keys"] = strings.Join(keys, ", ")
	case []interface{}:
		metadata["structure"] = "array"
		metadata["array_length"] = fmt.Sprintf("%d", len(v))
	default:
		metadata["structure"] = "primitive"
	}
}

// deduplicateTags removes duplicate tags.
func deduplicateTags(tags []string) []string {
	seen := make(map[string]bool)
	result := []string{}

	for _, tag := range tags {
		if !seen[tag] {
			seen[tag] = true
			result = append(result, tag)
		}
	}

	return result
}

// IsArchive checks if the content type represents an archive format.
func IsArchive(contentType string) bool {
	archiveTypes := []string{
		"application/zip",
		"application/x-tar",
		"application/gzip",
		"application/x-gzip",
		"application/x-compressed-tar",
	}

	for _, t := range archiveTypes {
		if strings.Contains(contentType, t) {
			return true
		}
	}

	// Also check by file extension patterns
	return strings.HasSuffix(contentType, "tar.gz") ||
		strings.HasSuffix(contentType, "tgz")
}

// ExtractArchive extracts an archive to a destination directory.
// Returns a list of extracted file paths.
func ExtractArchive(archivePath, destDir string) ([]string, error) {
	// Detect archive type from extension
	ext := strings.ToLower(filepath.Ext(archivePath))
	baseName := filepath.Base(archivePath)

	// Handle .tar.gz special case
	if strings.HasSuffix(strings.ToLower(baseName), ".tar.gz") {
		return extractTarGz(archivePath, destDir)
	}

	switch ext {
	case ".zip":
		return extractZip(archivePath, destDir)
	case ".tar":
		return extractTar(archivePath, destDir)
	case ".gz", ".gzip":
		// Check if it's a .tar.gz
		if strings.HasSuffix(strings.TrimSuffix(baseName, ext), ".tar") {
			return extractTarGz(archivePath, destDir)
		}
		// Single file gzip
		return extractGzip(archivePath, destDir)
	default:
		return nil, fmt.Errorf("unsupported archive format: %s", ext)
	}
}

// extractZip extracts a ZIP archive.
func extractZip(archivePath, destDir string) ([]string, error) {
	var extractedFiles []string

	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		// Skip directories
		if f.FileInfo().IsDir() {
			continue
		}

		// Prevent zip slip vulnerability
		destPath := filepath.Join(destDir, f.Name)
		if !strings.HasPrefix(destPath, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("illegal file path in archive: %s", f.Name)
		}

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(destPath), 0750); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}

		// Extract file
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open file in archive: %w", err)
		}

		outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
		if err != nil {
			rc.Close()
			return nil, fmt.Errorf("failed to create output file: %w", err)
		}

		_, err = io.Copy(outFile, rc)
		rc.Close()
		outFile.Close()

		if err != nil {
			return nil, fmt.Errorf("failed to extract file: %w", err)
		}

		extractedFiles = append(extractedFiles, destPath)
	}

	return extractedFiles, nil
}

// extractTar extracts a TAR archive.
func extractTar(archivePath, destDir string) ([]string, error) {
	var extractedFiles []string

	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open tar: %w", err)
	}
	defer f.Close()

	tr := tar.NewReader(f)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read tar entry: %w", err)
		}

		// Skip directories
		if header.Typeflag == tar.TypeDir {
			continue
		}

		// Only handle regular files
		if header.Typeflag != tar.TypeReg {
			continue
		}

		// Prevent path traversal
		destPath := filepath.Join(destDir, header.Name)
		if !strings.HasPrefix(destPath, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("illegal file path in archive: %s", header.Name)
		}

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(destPath), 0750); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}

		// Extract file
		outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
		if err != nil {
			return nil, fmt.Errorf("failed to create output file: %w", err)
		}

		_, err = io.Copy(outFile, tr)
		outFile.Close()

		if err != nil {
			return nil, fmt.Errorf("failed to extract file: %w", err)
		}

		extractedFiles = append(extractedFiles, destPath)
	}

	return extractedFiles, nil
}

// extractTarGz extracts a gzipped TAR archive.
func extractTarGz(archivePath, destDir string) ([]string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open tar.gz: %w", err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzr.Close()

	var extractedFiles []string
	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read tar entry: %w", err)
		}

		// Skip directories
		if header.Typeflag == tar.TypeDir {
			continue
		}

		// Only handle regular files
		if header.Typeflag != tar.TypeReg {
			continue
		}

		// Prevent path traversal
		destPath := filepath.Join(destDir, header.Name)
		if !strings.HasPrefix(destPath, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("illegal file path in archive: %s", header.Name)
		}

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(destPath), 0750); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}

		// Extract file
		outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
		if err != nil {
			return nil, fmt.Errorf("failed to create output file: %w", err)
		}

		_, err = io.Copy(outFile, tr)
		outFile.Close()

		if err != nil {
			return nil, fmt.Errorf("failed to extract file: %w", err)
		}

		extractedFiles = append(extractedFiles, destPath)
	}

	return extractedFiles, nil
}

// extractGzip extracts a single gzipped file.
func extractGzip(archivePath, destDir string) ([]string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open gzip: %w", err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzr.Close()

	// Output filename is the archive name without .gz extension
	baseName := filepath.Base(archivePath)
	outName := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	destPath := filepath.Join(destDir, outName)

	outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, gzr); err != nil {
		return nil, fmt.Errorf("failed to extract gzip: %w", err)
	}

	return []string{destPath}, nil
}
