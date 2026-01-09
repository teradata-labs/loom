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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsArchive(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		expected    bool
	}{
		{
			name:        "zip archive",
			contentType: "application/zip",
			expected:    true,
		},
		{
			name:        "tar archive",
			contentType: "application/x-tar",
			expected:    true,
		},
		{
			name:        "gzip archive",
			contentType: "application/gzip",
			expected:    true,
		},
		{
			name:        "tar.gz archive",
			contentType: "application/tar.gz",
			expected:    true,
		},
		{
			name:        "text file",
			contentType: "text/plain",
			expected:    false,
		},
		{
			name:        "json file",
			contentType: "application/json",
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsArchive(tt.contentType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractZip(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Create a test zip file
	zipPath := filepath.Join(tmpDir, "test.zip")
	zipFile, err := os.Create(zipPath)
	require.NoError(t, err)

	w := zip.NewWriter(zipFile)

	// Add test files to the zip
	testFiles := map[string]string{
		"file1.txt":     "content of file 1",
		"file2.txt":     "content of file 2",
		"dir/file3.txt": "content of file 3",
	}

	for name, content := range testFiles {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(content))
		require.NoError(t, err)
	}

	err = w.Close()
	require.NoError(t, err)
	err = zipFile.Close()
	require.NoError(t, err)

	// Extract the zip file
	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0750)
	require.NoError(t, err)

	extractedFiles, err := extractZip(zipPath, destDir)
	require.NoError(t, err)

	// Verify extracted files
	assert.Len(t, extractedFiles, 3)

	for name, expectedContent := range testFiles {
		extractedPath := filepath.Join(destDir, name)
		content, err := os.ReadFile(extractedPath)
		require.NoError(t, err)
		assert.Equal(t, expectedContent, string(content))
	}
}

func TestExtractTar(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Create a test tar file
	tarPath := filepath.Join(tmpDir, "test.tar")
	tarFile, err := os.Create(tarPath)
	require.NoError(t, err)

	w := tar.NewWriter(tarFile)

	// Add test files to the tar
	testFiles := map[string]string{
		"file1.txt": "content of file 1",
		"file2.txt": "content of file 2",
	}

	for name, content := range testFiles {
		hdr := &tar.Header{
			Name: name,
			Mode: 0640,
			Size: int64(len(content)),
		}
		err := w.WriteHeader(hdr)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}

	err = w.Close()
	require.NoError(t, err)
	err = tarFile.Close()
	require.NoError(t, err)

	// Extract the tar file
	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0750)
	require.NoError(t, err)

	extractedFiles, err := extractTar(tarPath, destDir)
	require.NoError(t, err)

	// Verify extracted files
	assert.Len(t, extractedFiles, 2)

	for name, expectedContent := range testFiles {
		extractedPath := filepath.Join(destDir, name)
		content, err := os.ReadFile(extractedPath)
		require.NoError(t, err)
		assert.Equal(t, expectedContent, string(content))
	}
}

func TestExtractTarGz(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Create a test tar.gz file
	tarGzPath := filepath.Join(tmpDir, "test.tar.gz")
	tarGzFile, err := os.Create(tarGzPath)
	require.NoError(t, err)

	gzw := gzip.NewWriter(tarGzFile)
	tw := tar.NewWriter(gzw)

	// Add test files to the tar
	testFiles := map[string]string{
		"file1.txt": "content of file 1",
		"file2.txt": "content of file 2",
	}

	for name, content := range testFiles {
		hdr := &tar.Header{
			Name: name,
			Mode: 0640,
			Size: int64(len(content)),
		}
		err := tw.WriteHeader(hdr)
		require.NoError(t, err)
		_, err = tw.Write([]byte(content))
		require.NoError(t, err)
	}

	err = tw.Close()
	require.NoError(t, err)
	err = gzw.Close()
	require.NoError(t, err)
	err = tarGzFile.Close()
	require.NoError(t, err)

	// Extract the tar.gz file
	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0750)
	require.NoError(t, err)

	extractedFiles, err := extractTarGz(tarGzPath, destDir)
	require.NoError(t, err)

	// Verify extracted files
	assert.Len(t, extractedFiles, 2)

	for name, expectedContent := range testFiles {
		extractedPath := filepath.Join(destDir, name)
		content, err := os.ReadFile(extractedPath)
		require.NoError(t, err)
		assert.Equal(t, expectedContent, string(content))
	}
}

func TestExtractArchive_PreventPathTraversal(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Create a malicious zip file with path traversal attempt
	zipPath := filepath.Join(tmpDir, "malicious.zip")
	zipFile, err := os.Create(zipPath)
	require.NoError(t, err)

	w := zip.NewWriter(zipFile)

	// Try to add a file with path traversal
	_, err = w.Create("../../../etc/passwd")
	require.NoError(t, err)

	err = w.Close()
	require.NoError(t, err)
	err = zipFile.Close()
	require.NoError(t, err)

	// Attempt to extract the zip file
	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0750)
	require.NoError(t, err)

	_, err = extractZip(zipPath, destDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "illegal file path")
}

func TestExtractGzip(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Create a test gzip file
	gzPath := filepath.Join(tmpDir, "test.txt.gz")
	gzFile, err := os.Create(gzPath)
	require.NoError(t, err)

	gzw := gzip.NewWriter(gzFile)
	content := "test content for gzip"
	_, err = gzw.Write([]byte(content))
	require.NoError(t, err)
	err = gzw.Close()
	require.NoError(t, err)
	err = gzFile.Close()
	require.NoError(t, err)

	// Extract the gzip file
	destDir := filepath.Join(tmpDir, "extracted")
	err = os.MkdirAll(destDir, 0750)
	require.NoError(t, err)

	extractedFiles, err := extractGzip(gzPath, destDir)
	require.NoError(t, err)

	// Verify extracted file
	assert.Len(t, extractedFiles, 1)

	extractedPath := extractedFiles[0]
	extractedContent, err := os.ReadFile(extractedPath)
	require.NoError(t, err)
	assert.Equal(t, content, string(extractedContent))
}

func TestInferTags_Archive(t *testing.T) {
	analyzer := NewAnalyzer()

	tests := []struct {
		name        string
		filename    string
		contentType string
		expected    []string
	}{
		{
			name:        "zip file",
			filename:    "data.zip",
			contentType: "application/zip",
			expected:    []string{"archive", "zip", "data"},
		},
		{
			name:        "tar file",
			filename:    "backup.tar",
			contentType: "application/x-tar",
			expected:    []string{"archive", "tar"},
		},
		{
			name:        "gzip file",
			filename:    "report.gz",
			contentType: "application/gzip",
			expected:    []string{"archive", "compressed", "report"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := analyzer.inferTags(tt.filename, tt.contentType)
			for _, expectedTag := range tt.expected {
				assert.Contains(t, tags, expectedTag, "Expected tag %s not found in %v", expectedTag, tags)
			}
		})
	}
}
