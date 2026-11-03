package handlers

import (
	"net/http"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func requireCurrentRevision(w http.ResponseWriter, manager *project.Manager, revision string) bool {
	index, err := manager.Index()
	if err != nil {
		writeProjectError(w, "project revision check failed", err)
		return false
	}
	if revision == "" || revision != index.ProjectRevision {
		writeProjectError(w, "project revision check failed", project.ErrRevisionConflict)
		return false
	}
	return true
}
