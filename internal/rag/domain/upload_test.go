package domain

import (
	"bytes"
	"strings"
	"testing"
)

func TestNormalizeUploadCanonicalizesBOMAndNewlines(t *testing.T) {
	upload, err := NormalizeUpload("CATALOG.MD", append([]byte{0xef, 0xbb, 0xbf}, []byte("# 商品\r\n颜色：黑色\r")...), 5<<20)
	if err != nil {
		t.Fatal(err)
	}
	if upload.FileName != "CATALOG.MD" {
		t.Fatalf("FileName = %q", upload.FileName)
	}
	if got := string(upload.Markdown); got != "# 商品\n颜色：黑色\n" {
		t.Fatalf("Markdown = %q", got)
	}
	if upload.ContentHash == "" || upload.SourceBytes != int64(len(upload.Markdown)) {
		t.Fatalf("upload metadata = %+v", upload)
	}
}

func TestNormalizeUploadRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name string
		file string
		raw  []byte
		code string
	}{
		{name: "extension", file: "catalog.txt", raw: []byte("text"), code: CodeUnsupportedFileType},
		{name: "empty", file: "catalog.md", raw: []byte(" \n"), code: CodeEmptyDocument},
		{name: "nul", file: "catalog.md", raw: []byte("a\x00b"), code: CodeInvalidMarkdownEncoding},
		{name: "utf8", file: "catalog.md", raw: []byte{0xff}, code: CodeInvalidMarkdownEncoding},
		{name: "large", file: "catalog.md", raw: bytes.Repeat([]byte("x"), 11), code: CodeFileTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeUpload(tc.file, tc.raw, 10)
			if !IsCode(err, tc.code) {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestValidateDocumentKey(t *testing.T) {
	got, err := ValidateDocumentKey(" 商品/iphone-16_pro ")
	if err != nil || got != "商品/iphone-16_pro" {
		t.Fatalf("ValidateDocumentKey() = %q, %v", got, err)
	}
	for _, key := range []string{"", "has space", "bad!", strings.Repeat("a", 129)} {
		if _, err := ValidateDocumentKey(key); !IsCode(err, CodeInvalidDocumentKey) {
			t.Fatalf("key %q error = %v", key, err)
		}
	}
}
