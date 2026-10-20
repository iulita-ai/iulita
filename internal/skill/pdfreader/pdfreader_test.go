package pdfreader

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/skill"
)

func TestRejectUntrustedInputs(t *testing.T) {
	for _, input := range []string{`{"url":"http://127.0.0.1/"}`, `{"attachment_index":-1}`, `{} {}`} {
		if _, err := New().Execute(context.Background(), json.RawMessage(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if _, err := Extract(context.Background(), skill.DocumentAttachment{Data: []byte("not a pdf")}); err == nil {
		t.Fatal("accepted non-PDF")
	}
	if _, err := Extract(context.Background(), skill.DocumentAttachment{Data: make([]byte, MaxPDFBytes+1)}); err == nil {
		t.Fatal("accepted oversized PDF")
	}
}
func TestPageBoundaries(t *testing.T) {
	doc, err := parseText("input.pdf", []byte("first\fsecond\f"))
	if err != nil || doc.Pages != 2 || !strings.Contains(doc.Text, "[Page 2]\nsecond") {
		t.Fatalf("doc=%+v err=%v", doc, err)
	}
	partial, err := parseText("mixed.pdf", []byte("first\f\f"))
	if err != nil || !strings.Contains(partial.Text, "require OCR") || partial.Pages != 2 {
		t.Fatalf("missing scan warning: %+v %v", partial, err)
	}
	for _, raw := range []string{" \n\f", strings.Repeat("page\f", 201)} {
		if _, err := parseText("x", []byte(raw)); err == nil {
			t.Fatal("accepted empty or excessive pages")
		}
	}
}
func TestBoundedOutputCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &boundedOutput{limit: 3, cancel: cancel}
	_, _ = out.Write([]byte("abcdef"))
	_, _ = out.Write([]byte("ghi"))
	if out.buf.String() != "abc" || !out.exceeded || ctx.Err() == nil {
		t.Fatal("output not bounded")
	}
}

func TestExtractRealPDF(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	objects := make([]string, 0, 5)
	objects = append(objects, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}...)
	stream := "BT /F1 12 Tf 72 720 Td (Example quarterly revenue is 42.) Tj ET\n"
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	var pdf strings.Builder
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 1, 1+len(objects))
	for i, obj := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	start := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), start)
	doc, err := Extract(context.Background(), skill.DocumentAttachment{Filename: "../../example.pdf", Data: []byte(pdf.String())})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Pages != 1 || !strings.Contains(doc.Text, "quarterly revenue is 42") {
		t.Fatalf("unexpected extraction: %+v", doc)
	}
}
