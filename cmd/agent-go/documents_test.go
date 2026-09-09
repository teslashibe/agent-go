package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/teslashibe/imessage"
	"golang.org/x/sys/unix"
)

func TestDocumentType(t *testing.T) {
	for _, tc := range []struct {
		name       string
		attachment imessage.Attachment
		kind       documentKind
		relevant   bool
	}{
		{name: "pdf MIME", attachment: imessage.Attachment{MIMEType: "application/pdf"}, kind: documentPDF, relevant: true},
		{name: "Word", attachment: imessage.Attachment{TransferName: "brief.docx"}, kind: documentRich, relevant: true},
		{name: "Markdown", attachment: imessage.Attachment{TransferName: "brief.md"}, kind: documentPlain, relevant: true},
		{name: "unsupported spreadsheet", attachment: imessage.Attachment{TransferName: "data.xlsx"}, kind: documentUnsupported, relevant: true},
		{name: "image ignored", attachment: imessage.Attachment{TransferName: "photo.jpg", MIMEType: "image/jpeg"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, relevant := documentType(tc.attachment)
			if kind != tc.kind || relevant != tc.relevant {
				t.Fatalf("documentType() = (%v, %v), want (%v, %v)", kind, relevant, tc.kind, tc.relevant)
			}
		})
	}
}

func TestSnapshotDocumentConfinesMessagesPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "brief.txt")
	if err := os.WriteFile(path, []byte("document text"), 0600); err != nil {
		t.Fatal(err)
	}
	attachment := imessage.Attachment{
		TransferName: "brief.txt", OriginalPath: path, TotalBytes: int64(len("document text")),
	}
	snapshot, err := snapshotDocument(root, attachment, "brief.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.remove()
	if snapshot.size != int64(len("document text")) || len(snapshot.digest) != 64 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if info, err := os.Stat(snapshot.path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private snapshot mode: %v, %v", info, err)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	attachment.OriginalPath = outside
	attachment.TotalBytes = int64(len("outside"))
	if _, err := snapshotDocument(root, attachment, "brief.txt"); err == nil {
		t.Fatal("outside attachment path accepted")
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	attachment.OriginalPath = link
	if _, err := snapshotDocument(root, attachment, "brief.txt"); err == nil {
		t.Fatal("symlink attachment accepted")
	}
	linkDir := filepath.Join(root, "linked")
	if err := os.Symlink(filepath.Dir(outside), linkDir); err != nil {
		t.Fatal(err)
	}
	attachment.OriginalPath = filepath.Join(linkDir, filepath.Base(outside))
	if _, err := snapshotDocument(root, attachment, "brief.txt"); err == nil {
		t.Fatal("symlink path component accepted")
	}
	fifo := filepath.Join(root, "pipe.txt")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	attachment.OriginalPath = fifo
	if _, err := snapshotDocument(root, attachment, "brief.txt"); err == nil {
		t.Fatal("FIFO attachment accepted")
	}
}

func TestBoundedText(t *testing.T) {
	if text, truncated, err := boundedText([]byte("hello\r\nworld"), false); err != nil || text != "hello\r\nworld" || truncated {
		t.Fatalf("boundedText() = %q, %v, %v", text, truncated, err)
	}
	if _, _, err := boundedText([]byte{'a', 0, 'b'}, false); err == nil {
		t.Fatal("NUL text accepted")
	}
	if _, _, err := boundedText([]byte{0xff}, false); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	text, truncated, err := boundedText([]byte(strings.Repeat("x", maxDocumentTextBytes+1)), false)
	if err != nil || len(text) != maxDocumentTextBytes || !truncated {
		t.Fatalf("truncation = %d, %v, %v", len(text), truncated, err)
	}
}

func TestExtractDocumentsAtRoot(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) imessage.Attachment {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return imessage.Attachment{
			TransferName: name, OriginalPath: path, TotalBytes: int64(len(content)),
		}
	}
	attachments := []imessage.Attachment{
		write("plain.txt", "plain document"),
		write("unsupported.xlsx", "not a spreadsheet parser fixture"),
		{TransferName: "photo.jpg", MIMEType: "image/jpeg"},
	}
	documents := extractDocumentsAtRoot(context.Background(), root, attachments)
	if len(documents) != 2 {
		t.Fatalf("documents = %+v", documents)
	}
	if documents[0].Text != "plain document" || documents[0].SHA256 == "" || documents[0].Error != "" {
		t.Fatalf("plain document = %+v", documents[0])
	}
	if documents[1].Error == "" || documents[1].Text != "" {
		t.Fatalf("unsupported document = %+v", documents[1])
	}
	tooMany := make([]imessage.Attachment, 5)
	for i := range tooMany {
		tooMany[i] = write(fmt.Sprintf("document-%d.txt", i), "text")
	}
	documents = extractDocumentsAtRoot(context.Background(), root, tooMany)
	if len(documents) != 1 || !strings.Contains(documents[0].Error, "more than four") {
		t.Fatalf("too many documents = %+v", documents)
	}
}

func TestNativeDocumentExtractors(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("PDFKit and textutil require macOS")
	}
	root := t.TempDir()
	rtf := `{\rtf1\ansi Rich attachment context}`
	rtfPath := filepath.Join(root, "brief.rtf")
	if err := os.WriteFile(rtfPath, []byte(rtf), 0600); err != nil {
		t.Fatal(err)
	}
	docxPath := filepath.Join(root, "brief.docx")
	if output, err := exec.Command("/usr/bin/textutil", "-convert", "docx", "-output", docxPath, rtfPath).CombinedOutput(); err != nil {
		t.Fatalf("create DOCX fixture: %v: %s", err, output)
	}
	docxInfo, err := os.Stat(docxPath)
	if err != nil {
		t.Fatal(err)
	}
	pdf := minimalPDF("PDF attachment context")
	pdfPath := filepath.Join(root, "brief.pdf")
	if err := os.WriteFile(pdfPath, pdf, 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	html := `<html><body>HTML attachment context<img src="` + server.URL + `/pixel.png"></body></html>`
	htmlPath := filepath.Join(root, "brief.html")
	if err := os.WriteFile(htmlPath, []byte(html), 0600); err != nil {
		t.Fatal(err)
	}
	documents := extractDocumentsAtRoot(context.Background(), root, []imessage.Attachment{
		{TransferName: "brief.rtf", OriginalPath: rtfPath, TotalBytes: int64(len(rtf))},
		{TransferName: "brief.docx", OriginalPath: docxPath, TotalBytes: docxInfo.Size()},
		{TransferName: "brief.pdf", MIMEType: "application/pdf", OriginalPath: pdfPath, TotalBytes: int64(len(pdf))},
		{TransferName: "brief.html", MIMEType: "text/html", OriginalPath: htmlPath, TotalBytes: int64(len(html))},
	})
	if len(documents) != 4 {
		t.Fatalf("documents = %+v", documents)
	}
	if documents[0].Error != "" || !strings.Contains(documents[0].Text, "Rich attachment context") {
		t.Fatalf("RTF extraction = %+v", documents[0])
	}
	if documents[1].Error != "" || !strings.Contains(documents[1].Text, "Rich attachment context") {
		t.Fatalf("DOCX extraction = %+v", documents[1])
	}
	if documents[2].Error != "" || !strings.Contains(documents[2].Text, "PDF attachment context") {
		t.Fatalf("PDF extraction = %+v", documents[2])
	}
	if documents[3].Error != "" || !strings.Contains(documents[3].Text, "HTML attachment context") || requests.Load() != 0 {
		t.Fatalf("HTML extraction = %+v, subsidiary requests = %d", documents[3], requests.Load())
	}
}

func TestLivePDFExtraction(t *testing.T) {
	path := os.Getenv("AGENT_DOCUMENT_LIVE_PDF")
	root := os.Getenv("AGENT_DOCUMENT_LIVE_ROOT")
	if path == "" || root == "" {
		t.Skip("live PDF extraction not enabled")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	documents := extractDocumentsAtRoot(context.Background(), root, []imessage.Attachment{{
		TransferName: "document.pdf", MIMEType: "application/pdf",
		OriginalPath: path, TotalBytes: info.Size(),
	}})
	if len(documents) != 1 {
		t.Fatalf("live PDF extraction returned %d documents", len(documents))
	}
	document := documents[0]
	if document.Error != "" || document.Text == "" || document.SHA256 == "" || document.Size != info.Size() {
		t.Fatalf("live PDF extraction failed: error=%q text_bytes=%d digest=%t size=%d",
			document.Error, len(document.Text), document.SHA256 != "", document.Size)
	}
	t.Logf("extracted %d bytes from a %d-byte PDF; truncated=%t", len(document.Text), document.Size, document.Truncated)
}

func minimalPDF(text string) []byte {
	text = strings.NewReplacer("\\", `\\`, "(", `\(`, ")", `\)`).Replace(text)
	stream := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
	}
	var output strings.Builder
	output.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(output.String())
}
