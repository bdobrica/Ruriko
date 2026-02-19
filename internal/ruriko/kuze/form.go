package kuze

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"
)

//go:embed templates/*.html
var templateFS embed.FS

var (
	tmplForm    = mustParse("templates/form.html")
	tmplSuccess = mustParse("templates/success.html")
	tmplExpired = mustParse("templates/expired.html")
)

func mustParse(name string) *template.Template {
	content, err := templateFS.ReadFile(name)
	if err != nil {
		panic("kuze: missing embedded template " + name + ": " + err.Error())
	}
	t, err := template.New(name).Parse(string(content))
	if err != nil {
		panic("kuze: parse template " + name + ": " + err.Error())
	}
	return t
}

// formData is passed to form.html.
type formData struct {
	SecretRef string
	Token     string
	Error     string
}

// successData is passed to success.html.
type successData struct {
	SecretRef string
}

func renderForm(w http.ResponseWriter, secretRef, token string) {
	renderFormWithError(w, secretRef, token, "")
}

func renderFormWithError(w http.ResponseWriter, secretRef, token, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplForm.Execute(w, formData{
		SecretRef: secretRef,
		Token:     token,
		Error:     errMsg,
	}); err != nil {
		slog.Error("kuze: render form template", "err", err)
	}
}

func renderSuccessPage(w http.ResponseWriter, secretRef string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplSuccess.Execute(w, successData{SecretRef: secretRef}); err != nil {
		slog.Error("kuze: render success template", "err", err)
	}
}

func renderExpiredPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusGone)
	if err := tmplExpired.Execute(w, nil); err != nil {
		slog.Error("kuze: render expired template", "err", err)
	}
}
