package handlers

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/user/bitcreds/internal/db"
	"github.com/user/bitcreds/internal/patterns"
)

//go:embed templates/*
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type Handler struct {
	db       *db.DB
	patterns []patterns.CompiledPattern
	tmpl     *template.Template
}

func New(database *db.DB, pats []patterns.CompiledPattern) *Handler {
	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"seq": func(start, end int) []int {
			s := make([]int, 0, end-start+1)
			for i := start; i <= end; i++ {
				s = append(s, i)
			}
			return s
		},
		"severityClass": func(s string) string {
			switch s {
			case "critical":
				return "severity-critical"
			case "high":
				return "severity-high"
			case "medium":
				return "severity-medium"
			default:
				return "severity-low"
			}
		},
		"statusClass": func(s string) string {
			switch s {
			case "open":
				return "status-open"
			case "resolved":
				return "status-resolved"
			case "false_positive":
				return "status-fp"
			default:
				return ""
			}
		},
		"truncate": func(s string, n int) string {
			if len(s) > n {
				return s[:n] + "..."
			}
			return s
		},
	}

	tmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html"))
	return &Handler{
		db:       database,
		patterns: pats,
		tmpl:     tmpl,
	}
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	stats, err := h.db.GetDashboardStats()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		log.Printf("Dashboard error: %v", err)
		return
	}

	h.render(w, "dashboard.html", stats)
}

func (h *Handler) FindingsList(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 50

	filter := db.FindingFilter{
		Status:      r.URL.Query().Get("status"),
		ProjectKey:  r.URL.Query().Get("project"),
		RepoSlug:    r.URL.Query().Get("repo"),
		PatternName: r.URL.Query().Get("pattern"),
		Limit:       perPage,
		Offset:      (page - 1) * perPage,
	}

	findings, total, err := h.db.ListFindings(filter)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		log.Printf("Findings list error: %v", err)
		return
	}

	projects, _ := h.db.GetDistinctProjects()
	patternNames, _ := h.db.GetDistinctPatterns()

	totalPages := (total + perPage - 1) / perPage

	data := map[string]interface{}{
		"Findings":    findings,
		"Total":       total,
		"Page":        page,
		"TotalPages":  totalPages,
		"Filter":      filter,
		"Projects":    projects,
		"PatternNames": patternNames,
	}

	h.render(w, "findings.html", data)
}

func (h *Handler) FindingDetail(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/findings/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	finding, err := h.db.GetFinding(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	notes, _ := h.db.GetNotes(id)

	data := map[string]interface{}{
		"Finding": finding,
		"Notes":   notes,
	}

	h.render(w, "finding_detail.html", data)
}

func (h *Handler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	status := r.FormValue("status")

	if status != "open" && status != "resolved" && status != "false_positive" {
		http.Error(w, "Invalid status", http.StatusBadRequest)
		return
	}

	if err := h.db.UpdateFindingStatus(id, status); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	redirect := r.FormValue("redirect")
	if redirect == "" {
		redirect = fmt.Sprintf("/findings/%d", id)
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func (h *Handler) BulkAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	action := r.FormValue("action")
	idsStr := r.Form["ids"]

	var ids []int64
	for _, s := range idsStr {
		id, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			ids = append(ids, id)
		}
	}

	if len(ids) == 0 {
		http.Redirect(w, r, "/findings", http.StatusSeeOther)
		return
	}

	switch action {
	case "resolve":
		h.db.BulkUpdateFindingStatus(ids, "resolved")
	case "false_positive":
		h.db.BulkUpdateFindingStatus(ids, "false_positive")
	case "reopen":
		h.db.BulkUpdateFindingStatus(ids, "open")
	case "delete":
		h.db.BulkDeleteFindings(ids)
	}

	http.Redirect(w, r, "/findings", http.StatusSeeOther)
}

func (h *Handler) DeleteFinding(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)

	if err := h.db.DeleteFinding(id); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/findings", http.StatusSeeOther)
}

func (h *Handler) AddNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	findingID, _ := strconv.ParseInt(r.FormValue("finding_id"), 10, 64)
	content := strings.TrimSpace(r.FormValue("content"))

	if content == "" {
		http.Redirect(w, r, fmt.Sprintf("/findings/%d", findingID), http.StatusSeeOther)
		return
	}

	h.db.AddNote(findingID, content)
	http.Redirect(w, r, fmt.Sprintf("/findings/%d", findingID), http.StatusSeeOther)
}

func (h *Handler) DeleteNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	noteID, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	findingID, _ := strconv.ParseInt(r.FormValue("finding_id"), 10, 64)

	h.db.DeleteNote(noteID)
	http.Redirect(w, r, fmt.Sprintf("/findings/%d", findingID), http.StatusSeeOther)
}

func (h *Handler) ScansList(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	scans, err := h.db.ListScans(50, (page-1)*50)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	h.render(w, "scans.html", map[string]interface{}{
		"Scans": scans,
		"Page":  page,
	})
}

func (h *Handler) PatternsList(w http.ResponseWriter, r *http.Request) {
	h.render(w, "patterns.html", map[string]interface{}{
		"Patterns": h.patterns,
	})
}

func (h *Handler) Static(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	data, err := staticFS.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if strings.HasSuffix(path, ".css") {
		w.Header().Set("Content-Type", "text/css")
	} else if strings.HasSuffix(path, ".js") {
		w.Header().Set("Content-Type", "application/javascript")
	}

	w.Write(data)
}

func (h *Handler) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("Template render error (%s): %v", name, err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}
