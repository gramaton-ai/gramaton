package server

import (
	"net/http"
)

func (s *Server) handleCollectionCreate(w http.ResponseWriter, r *http.Request) {
	var req collectionCreateRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}
	result, svcErr := s.serviceCollectionCreate(r.Context(), &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleCollectionList(w http.ResponseWriter, r *http.Request) {
	result, svcErr := s.serviceCollectionList()
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCollectionItems(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	req := &collectionItemsRequest{
		Sort:           r.URL.Query().Get("sort"),
		Order:          r.URL.Query().Get("order"),
		IncludeRetired: r.URL.Query().Get("include_retired") == "true",
	}
	result, svcErr := s.serviceCollectionItems(id, req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCollectionAdd(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req collectionAddRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}
	result, svcErr := s.serviceCollectionAdd(id, &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleCollectionUpdateItem(w http.ResponseWriter, r *http.Request) {
	collID := r.PathValue("id")
	itemID := r.PathValue("item_id")
	var req collectionUpdateRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}
	result, svcErr := s.serviceCollectionUpdate(collID, itemID, &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCollectionMoveItem(w http.ResponseWriter, r *http.Request) {
	collID := r.PathValue("id")
	itemID := r.PathValue("item_id")
	var req collectionMoveRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}
	result, svcErr := s.serviceCollectionMove(collID, itemID, &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCollectionRemoveItem(w http.ResponseWriter, r *http.Request) {
	collID := r.PathValue("id")
	itemID := r.PathValue("item_id")
	result, svcErr := s.serviceCollectionRemove(collID, itemID)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCollectionRename(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req collectionRenameRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}
	result, svcErr := s.serviceCollectionRename(id, &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCollectionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, svcErr := s.serviceCollectionDelete(id)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCollectionSchemaRead(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, svcErr := s.serviceCollectionSchemaRead(id)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCollectionSchemaUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req collectionSchemaUpdateRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}
	result, svcErr := s.serviceCollectionSchemaUpdate(id, &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCollectionMigrate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req collectionMigrateRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}
	result, svcErr := s.serviceCollectionMigrate(id, &req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}
