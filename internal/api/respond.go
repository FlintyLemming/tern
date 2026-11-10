package api

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
)

const maxBodyBytes = 1 << 20

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError sends the standard error body. code is the stable identifier the
// UI maps to localized text; message is English, for logs and API users.
func writeError(w http.ResponseWriter, status int, code, message string, fields map[string]string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message, Fields: fields}})
}

// readJSON decodes a JSON request body of at most 1 MiB into dst, refusing
// unknown fields and trailing data. On failure it writes a 400 and returns false.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusBadRequest, "bad_request", "Content-Type must be application/json", nil)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "request body is not valid JSON for this endpoint", nil)
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		writeError(w, http.StatusBadRequest, "bad_request", "request body has data after the JSON value", nil)
		return false
	}
	return true
}
