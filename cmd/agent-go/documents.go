package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teslashibe/agent-go/internal/bridge"
	"github.com/teslashibe/imessage"
	"golang.org/x/sys/unix"
)

const (
	maxDocumentsPerMessage = 4
	maxDocumentFileBytes   = 25 << 20
	maxDocumentTextBytes   = 128 << 10
	maxDocumentTotalBytes  = 192 << 10
)

type documentKind uint8

const (
	documentUnsupported documentKind = iota
	documentPlain
	documentRich
	documentPDF
)

func documentType(attachment imessage.Attachment) (documentKind, bool) {
	name := attachment.TransferName
	if name == "" {
		name = attachment.Filename
	}
	ext := strings.ToLower(filepath.Ext(name))
	mime := strings.ToLower(strings.TrimSpace(attachment.MIMEType))
	uti := strings.ToLower(strings.TrimSpace(attachment.UTI))
	if ext == ".pdf" || mime == "application/pdf" || uti == "com.adobe.pdf" {
		return documentPDF, true
	}
	switch ext {
	case ".txt", ".md", ".markdown", ".csv", ".tsv", ".json", ".xml", ".yaml", ".yml", ".log", ".eml":
		return documentPlain, true
	case ".rtf", ".doc", ".docx", ".odt", ".html", ".htm", ".webarchive":
		return documentRich, true
	case ".pages", ".key", ".numbers", ".xls", ".xlsx", ".ppt", ".pptx", ".epub":
		return documentUnsupported, true
	}
	if strings.HasPrefix(mime, "text/") {
		if mime == "text/html" || mime == "text/rtf" {
			return documentRich, true
		}
		return documentPlain, true
	}
	switch mime {
	case "application/rtf", "application/msword",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.oasis.opendocument.text":
		return documentRich, true
	}
	return documentUnsupported, false
}

func documentName(attachment imessage.Attachment) string {
	name := attachment.TransferName
	if name == "" {
		name = attachment.Filename
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." {
		name = "document"
	}
	for len(name) > 255 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

type documentSnapshot struct {
	path   string
	digest string
	size   int64
	remove func()
}

func openDocument(root, path string) (*os.File, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, errors.New("Messages attachment store is unavailable")
	}
	path = filepath.Clean(path)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, errors.New("attachment path is outside Messages")
	}
	parts := strings.Split(relative, string(filepath.Separator))
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("Messages attachment store is unavailable")
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, errors.New("attachment path cannot be opened safely")
		}
		fd = next
	}
	fileFD, err := unix.Openat(fd, parts[len(parts)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	_ = unix.Close(fd)
	if err != nil {
		return nil, errors.New("attachment cannot be opened")
	}
	return os.NewFile(uintptr(fileFD), path), nil
}

func snapshotDocument(root string, attachment imessage.Attachment, name string) (documentSnapshot, error) {
	if attachment.Missing || attachment.OriginalPath == "" || !filepath.IsAbs(attachment.OriginalPath) {
		return documentSnapshot{}, errors.New("attachment file is unavailable")
	}
	if attachment.TotalBytes < 0 || attachment.TotalBytes > maxDocumentFileBytes {
		return documentSnapshot{}, errors.New("attachment exceeds 25 MiB limit")
	}
	input, err := openDocument(root, attachment.OriginalPath)
	if err != nil {
		return documentSnapshot{}, err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return documentSnapshot{}, errors.New("attachment is not a regular file")
	}
	if opened.Size() > maxDocumentFileBytes {
		return documentSnapshot{}, errors.New("attachment exceeds 25 MiB limit")
	}
	if attachment.TotalBytes > 0 && opened.Size() != attachment.TotalBytes {
		return documentSnapshot{}, errors.New("attachment size does not match Messages metadata")
	}
	dir, err := os.MkdirTemp("", "agent-document-")
	if err != nil {
		return documentSnapshot{}, errors.New("private document snapshot cannot be created")
	}
	remove := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "document"+strings.ToLower(filepath.Ext(name)))
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		remove()
		return documentSnapshot{}, errors.New("private document snapshot cannot be created")
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, maxDocumentFileBytes+1))
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil || written != opened.Size() || written > maxDocumentFileBytes {
		remove()
		return documentSnapshot{}, errors.New("attachment snapshot failed")
	}
	return documentSnapshot{path: path, digest: hex.EncodeToString(hash.Sum(nil)), size: written, remove: remove}, nil
}

type boundedDocumentOutput struct {
	buffer   bytes.Buffer
	cancel   context.CancelFunc
	limit    int
	exceeded bool
}

func (b *boundedDocumentOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			_, _ = b.buffer.Write(data[:remaining])
		} else {
			_, _ = b.buffer.Write(data)
		}
	}
	if n > remaining {
		b.exceeded = true
		b.cancel()
	}
	return n, nil
}

func boundedCommand(ctx context.Context, limit int, name string, args ...string) ([]byte, bool, error) {
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output := &boundedDocumentOutput{cancel: cancel, limit: limit}
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if output.exceeded && output.buffer.Len() > 0 {
		return output.buffer.Bytes(), true, nil
	}
	if err != nil || runCtx.Err() != nil {
		return nil, false, errors.New("document conversion failed")
	}
	return output.buffer.Bytes(), false, nil
}

func boundedText(data []byte, alreadyTruncated bool) (string, bool, error) {
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", false, errors.New("document text is not valid UTF-8")
	}
	if strings.IndexFunc(string(data), func(r rune) bool {
		return r < 32 && r != '\n' && r != '\r' && r != '\t'
	}) >= 0 {
		return "", false, errors.New("document text contains unsupported control characters")
	}
	truncated := alreadyTruncated || len(data) > maxDocumentTextBytes
	if len(data) > maxDocumentTextBytes {
		data = data[:maxDocumentTextBytes]
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", false, errors.New("document contains no extractable text")
	}
	return text, truncated, nil
}

func extractPlain(snapshot documentSnapshot) (string, bool, error) {
	file, err := os.Open(snapshot.path)
	if err != nil {
		return "", false, errors.New("document snapshot cannot be read")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxDocumentTextBytes+utf8.UTFMax+1))
	if err != nil {
		return "", false, errors.New("document snapshot cannot be read")
	}
	return boundedText(data, snapshot.size > int64(len(data)))
}

func extractRich(ctx context.Context, snapshot documentSnapshot) (string, bool, error) {
	data, truncated, err := boundedCommand(ctx, maxDocumentTextBytes+utf8.UTFMax+1,
		"/usr/bin/textutil", "-convert", "txt", "-stdout", "-noload", snapshot.path)
	if err != nil {
		return "", false, err
	}
	return boundedText(data, truncated)
}

const pdfTextScript = `
ObjC.import("PDFKit");
function run(a){
  const document=$.PDFDocument.alloc.initWithURL($.NSURL.fileURLWithPath(a[0]));
  if(!document)throw Error("invalid PDF");
  if(document.isEncrypted&&!document.isUnlocked)throw Error("encrypted PDF");
  const pages=Number(document.pageCount);
  if(pages>500)throw Error("PDF page limit exceeded");
  const chunks=[]; let length=0, truncated=false;
  for(let i=0;i<pages;i++){
    const page=document.pageAtIndex(i), value=page?ObjC.unwrap(page.string):null;
    if(!value)continue;
    const remaining=262144-length;
    if(remaining<=0){truncated=true;break}
    chunks.push(value.slice(0,remaining)); length+=Math.min(value.length,remaining);
    if(value.length>remaining){truncated=true;break}
  }
  return JSON.stringify({text:chunks.join("\n\n"),truncated:truncated});
}`

func extractPDF(ctx context.Context, snapshot documentSnapshot) (string, bool, error) {
	header, err := os.Open(snapshot.path)
	if err != nil {
		return "", false, errors.New("PDF snapshot cannot be read")
	}
	var magic [5]byte
	_, readErr := io.ReadFull(header, magic[:])
	_ = header.Close()
	if readErr != nil || string(magic[:]) != "%PDF-" {
		return "", false, errors.New("attachment is not a valid PDF")
	}
	data, _, err := boundedCommand(ctx, 2<<20, "/usr/bin/osascript",
		"-l", "JavaScript", "-e", pdfTextScript, snapshot.path)
	if err != nil {
		return "", false, errors.New("PDF text extraction failed")
	}
	var result struct {
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	}
	if json.Unmarshal(data, &result) != nil {
		return "", false, errors.New("PDF extractor returned invalid output")
	}
	return boundedText([]byte(result.Text), result.Truncated)
}

func extractDocuments(ctx context.Context, attachments []imessage.Attachment) []bridge.AttachedDocument {
	if runtime.GOOS != "darwin" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return extractDocumentsAtRoot(ctx, filepath.Join(home, "Library", "Messages", "Attachments"), attachments)
}

func extractDocumentsAtRoot(ctx context.Context, root string, attachments []imessage.Attachment) []bridge.AttachedDocument {
	relevantAttachments := make([]imessage.Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		if _, relevant := documentType(attachment); relevant {
			relevantAttachments = append(relevantAttachments, attachment)
		}
	}
	if len(relevantAttachments) > maxDocumentsPerMessage {
		return []bridge.AttachedDocument{{
			Name: "documents", Error: "message contains more than four document attachments",
		}}
	}
	documents := make([]bridge.AttachedDocument, 0, len(relevantAttachments))
	total := 0
	for _, attachment := range relevantAttachments {
		kind, _ := documentType(attachment)
		mime := strings.TrimSpace(attachment.MIMEType)
		if len(mime) > 255 || strings.ContainsAny(mime, "\r\n\x00") {
			mime = ""
		}
		size := attachment.TotalBytes
		if size < 0 {
			size = 0
		}
		document := bridge.AttachedDocument{
			Name: documentName(attachment), MIMEType: mime, Size: size,
		}
		if kind == documentUnsupported {
			document.Error = "document format is not supported for text extraction"
			documents = append(documents, document)
			continue
		}
		snapshot, snapshotErr := snapshotDocument(root, attachment, document.Name)
		if snapshotErr != nil {
			document.Error = snapshotErr.Error()
			documents = append(documents, document)
			continue
		}
		document.SHA256, document.Size = snapshot.digest, snapshot.size
		var text string
		var truncated bool
		var extractErr error
		switch kind {
		case documentPlain:
			text, truncated, extractErr = extractPlain(snapshot)
		case documentRich:
			text, truncated, extractErr = extractRich(ctx, snapshot)
		case documentPDF:
			text, truncated, extractErr = extractPDF(ctx, snapshot)
		}
		snapshot.remove()
		if extractErr != nil {
			document.Error = extractErr.Error()
		} else if total >= maxDocumentTotalBytes {
			document.Error = "message document text budget is exhausted"
		} else {
			remaining := maxDocumentTotalBytes - total
			if len(text) > remaining {
				data := []byte(text)[:remaining]
				for len(data) > 0 && !utf8.Valid(data) {
					data = data[:len(data)-1]
				}
				text, truncated = strings.TrimSpace(string(data)), true
			}
			if text == "" {
				document.Error = "document contains no extractable text"
			} else {
				document.Text, document.Truncated = text, truncated
				total += len(text)
			}
		}
		documents = append(documents, document)
	}
	return documents
}
