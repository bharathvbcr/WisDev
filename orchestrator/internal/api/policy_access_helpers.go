package api

import (
	"net/http"
	"strings"
)

func requireInternalPolicyAccess(w http.ResponseWriter, r *http.Request) bool {
	switch strings.TrimSpace(GetUserID(r)) {
	case "admin", "internal-service":
		return true
	default:
		WriteError(w, http.StatusForbidden, ErrUnauthorized, "internal service access required", nil)
		return false
	}
}
