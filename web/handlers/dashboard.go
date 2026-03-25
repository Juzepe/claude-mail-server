package handlers

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mailserver/config"
	"mailserver/db"
	"mailserver/mail"
)

type serviceStatus struct {
	Name   string
	Active bool
	Status string
}

// auditEntry wraps db.AuditEntry with a computed CSS class for the action badge.
type auditEntry struct {
	ID          int64
	Action      string
	ActionClass string
	Target      string
	Detail      string
	IPAddr      string
	Timestamp   time.Time
}

type dashboardData struct {
	Domain        string
	AccountCount  int
	DiskUsage     string
	ServerSummary string
	Services      []serviceStatus
	AuditLog      []auditEntry
	Flash         string
	Error         string
}

// Dashboard handles GET / - shows the main admin dashboard.
func Dashboard(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only handle exact root path
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		users, err := mail.ListUsers(cfg)
		accountCount := 0
		if err == nil {
			accountCount = len(users)
		}

		diskUsage := getDiskUsage(cfg.MailDir)
		services := getServiceStatuses()

		// Compute summary
		allUp := true
		for _, s := range services {
			if !s.Active {
				allUp = false
				break
			}
		}
		summary := "Online"
		if !allUp {
			summary = "Issues"
		}

		rawLog, _ := db.GetRecentAuditLog(10)
		var auditLog []auditEntry
		for _, e := range rawLog {
			auditLog = append(auditLog, auditEntry{
				ID:          e.ID,
				Action:      e.Action,
				ActionClass: actionBadgeClass(e.Action),
				Target:      e.Target,
				Detail:      e.Detail,
				IPAddr:      e.IPAddr,
				Timestamp:   e.Timestamp,
			})
		}

		data := dashboardData{
			Domain:        cfg.Domain,
			AccountCount:  accountCount,
			DiskUsage:     diskUsage,
			ServerSummary: summary,
			Services:      services,
			AuditLog:      auditLog,
		}

		renderTemplate(w, "dashboard.html", data)
	}
}

func actionBadgeClass(action string) string {
	switch action {
	case "login":
		return "badge-info"
	case "login_failed":
		return "badge-error"
	case "add_user":
		return "badge-success"
	case "delete_user":
		return "badge-warning"
	case "logout":
		return "badge-secondary"
	default:
		return "badge-secondary"
	}
}

func getDiskUsage(dir string) string {
	cmd := exec.Command("du", "-sh", dir)
	output, err := cmd.Output()
	if err != nil {
		return "N/A"
	}
	parts := strings.Fields(string(output))
	if len(parts) > 0 {
		return parts[0]
	}
	return "N/A"
}

func getServiceStatuses() []serviceStatus {
	services := []string{"postfix", "dovecot", "opendkim"}
	var statuses []serviceStatus

	for _, svc := range services {
		cmd := exec.Command("systemctl", "is-active", svc)
		output, _ := cmd.Output()
		statusStr := strings.TrimSpace(string(output))
		active := statusStr == "active"
		if statusStr == "" {
			statusStr = "unknown"
		}
		statuses = append(statuses, serviceStatus{
			Name:   svc,
			Active: active,
			Status: statusStr,
		})
	}
	return statuses
}

// renderTemplate renders a named template with the layout.
func renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	dir := templateDir()
	layoutPath := filepath.Join(dir, "layout.html")
	pagePath := filepath.Join(dir, name)

	funcMap := template.FuncMap{
		"formatTime": func(t time.Time) string {
			return t.Format("Jan 2, 15:04")
		},
		"statusClass": func(active bool) string {
			if active {
				return "status-up"
			}
			return "status-down"
		},
		"safeHTML": func(s string) template.HTML {
			return template.HTML(s)
		},
	}

	tmpl, err := template.New("layout.html").Funcs(funcMap).ParseFiles(layoutPath, pagePath)
	if err != nil {
		log.Printf("Template parse error (%s): %v", name, err)
		http.Error(w, fmt.Sprintf("Template error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Execute the layout template (which uses blocks defined in the page template)
	if err := tmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		log.Printf("Template execute error (%s): %v", name, err)
	}
}
