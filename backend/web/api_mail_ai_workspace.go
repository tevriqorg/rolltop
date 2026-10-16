package web

import (
	"net/http"
	"strings"

	"rolltop/backend/store"
)

type mailAIWorkspaceRequest struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	MessageID int64  `json:"message_id"`
	Status    string `json:"status"`
	DueAt     string `json:"due_at"`
}

func (s *Server) apiMailAIWorkspace(w http.ResponseWriter, r *http.Request, cu currentUser) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ListMailAIWorkspaceItems(r.Context(), cu.User.ID, 100)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		writeJSON(w, map[string]any{"items": items})
	case http.MethodPost, http.MethodPut:
		if !s.verifyCSRF(w, r) {
			return
		}
		var body mailAIWorkspaceRequest
		if !decodeMailAIJSON(w, r, &body, 12<<10) {
			return
		}
		if body.MessageID > 0 {
			if _, err := s.store.GetMessageForUser(r.Context(), cu.User.ID, body.MessageID); err != nil {
				if store.IsNotFound(err) {
					writeAPIError(w, http.StatusNotFound, "workspace message not found")
				} else {
					s.serverError(w, r, err)
				}
				return
			}
		}
		item, err := s.store.UpsertMailAIWorkspaceItem(r.Context(), cu.User.ID, store.MailAIWorkspaceItem{
			ID: strings.TrimSpace(body.ID), Kind: body.Kind, Title: body.Title, Detail: body.Detail,
			MessageID: body.MessageID, Status: body.Status, DueAt: body.DueAt,
		})
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, item)
	default:
		methodNotAllowed(w)
	}
}
