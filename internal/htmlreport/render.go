package htmlreport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "embed"
)

//go:embed template.html
var pageTemplate string

type templateData struct {
	Payload   template.JS
	Generated string
	Version   string
}

// Render writes the self-contained report page. The result has no external
// requests, so it opens straight from disk and keeps working offline.
func Render(w io.Writer, data Data) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Escaping < > & keeps a directory name from closing the script element.
	enc.SetEscapeHTML(true)
	if err := enc.Encode(data); err != nil {
		return fmt.Errorf("encoding report data: %w", err)
	}

	tmpl, err := template.New("report").Parse(pageTemplate)
	if err != nil {
		return fmt.Errorf("parsing report template: %w", err)
	}

	return tmpl.Execute(w, templateData{
		// Encode appends a newline; trimming keeps the emitted statement tidy.
		Payload:   template.JS(bytes.TrimRight(buf.Bytes(), "\n")),
		Generated: time.Unix(data.Generated, 0).Format("2 Jan 2006, 15:04"),
		Version:   data.Version,
	})
}

// WriteFile renders the report to path, creating parent directories as needed.
func WriteFile(path string, data Data) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating report directory: %w", err)
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	defer f.Close()

	if err := Render(f, data); err != nil {
		return err
	}
	return f.Close()
}
