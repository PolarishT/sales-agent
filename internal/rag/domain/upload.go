package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

type Upload struct {
	DocumentKey string
	FileName    string
	Markdown    []byte
	ContentHash string
	SourceBytes int64
}

func NormalizeUpload(fileName string, raw []byte, maxBytes int64) (Upload, error) {
	if strings.TrimSpace(fileName) == "" {
		return Upload{}, NewError(CodeFileRequired, "必须提供 Markdown 文件", nil)
	}
	if !strings.HasSuffix(strings.ToLower(fileName), ".md") && !strings.HasSuffix(strings.ToLower(fileName), ".markdown") {
		return Upload{}, NewError(CodeUnsupportedFileType, "仅支持 .md 或 .markdown 文件", nil)
	}
	if int64(len(raw)) > maxBytes {
		return Upload{}, NewError(CodeFileTooLarge, "文件超过允许大小", nil)
	}
	if !utf8.Valid(raw) || bytes.Contains(raw, []byte{0}) {
		return Upload{}, NewError(CodeInvalidMarkdownEncoding, "Markdown 必须是有效的 UTF-8 编码", nil)
	}

	markdown := bytes.TrimPrefix(raw, utf8BOM)
	markdown = bytes.ReplaceAll(markdown, []byte("\r\n"), []byte("\n"))
	markdown = bytes.ReplaceAll(markdown, []byte("\r"), []byte("\n"))
	if strings.TrimSpace(string(markdown)) == "" {
		return Upload{}, NewError(CodeEmptyDocument, "Markdown 文档不能为空", nil)
	}

	hash := sha256.Sum256(markdown)
	return Upload{
		FileName:    fileName,
		Markdown:    markdown,
		ContentHash: hex.EncodeToString(hash[:]),
		SourceBytes: int64(len(markdown)),
	}, nil
}

func ValidateDocumentKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if length := utf8.RuneCountInString(key); length < 1 || length > 128 {
		return "", NewError(CodeInvalidDocumentKey, "document_key 必须包含 1 到 128 个字符", nil)
	}
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		switch r {
		case '.', '_', '/', '-':
			continue
		default:
			return "", NewError(CodeInvalidDocumentKey, "document_key 包含不支持的字符", nil)
		}
	}
	return key, nil
}
