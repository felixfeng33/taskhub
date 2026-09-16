package taskhub

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

func Handler(store *Store, token, version string) (http.Handler, error) {
	if len(token) < 16 || strings.TrimSpace(token) != token {
		return nil, errors.New("server token must contain at least 16 bytes and no surrounding whitespace")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks", func(w http.ResponseWriter, r *http.Request) {
		status := r.URL.Query().Get("status")
		if status != "" && !ValidStatus(status) {
			fail(w, 400, "invalid status")
			return
		}
		limit, after := 100, int64(0)
		var err error
		if value := r.URL.Query().Get("limit"); value != "" {
			limit, err = strconv.Atoi(value)
		}
		if err != nil || limit < 1 || limit > 1000 {
			fail(w, 400, "limit must be between 1 and 1000")
			return
		}
		if value := r.URL.Query().Get("after"); value != "" {
			after, err = strconv.ParseInt(value, 10, 64)
		}
		if err != nil || after < 0 {
			fail(w, 400, "after must be a nonnegative task ID")
			return
		}
		tasks, err := store.List(r.Context(), status, after, limit)
		if err != nil {
			storeError(w, err)
			return
		}
		writeJSON(w, 200, tasks)
	})
	mux.HandleFunc("POST /tasks", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Title  string `json:"title"`
			Body   string `json:"body"`
			Status string `json:"status"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Status == "" {
			input.Status = "pending"
		}
		if err := Validate(strings.TrimSpace(input.Title), input.Body, input.Status); err != nil {
			fail(w, 400, err.Error())
			return
		}
		t, err := store.Add(r.Context(), input.Title, input.Body, input.Status)
		if err != nil {
			storeError(w, err)
			return
		}
		w.Header().Set("Location", "/tasks/"+strconv.FormatInt(t.ID, 10))
		writeJSON(w, 201, t)
	})
	mux.HandleFunc("GET /tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := taskID(w, r)
		if !ok {
			return
		}
		t, err := store.Get(r.Context(), id)
		if err != nil {
			storeError(w, err)
			return
		}
		writeJSON(w, 200, t)
	})
	mux.HandleFunc("PATCH /tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := taskID(w, r)
		if !ok {
			return
		}
		var input Update
		if !decode(w, r, &input) {
			return
		}
		if err := input.Validate(); err != nil {
			fail(w, 400, err.Error())
			return
		}
		t, err := store.Update(r.Context(), id, input)
		if err != nil {
			storeError(w, err)
			return
		}
		writeJSON(w, 200, t)
	})
	expected := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == "GET" && r.URL.Path == "/healthz" {
			writeJSON(w, 200, map[string]string{"status": "ok", "version": version})
			return
		}
		actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, 401, "unauthorized")
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func taskID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		fail(w, 400, "id must be a positive integer")
		return 0, false
	}
	return id, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	// JSON escaping can expand a one-byte body character to six bytes.
	r.Body = http.MaxBytesReader(w, r.Body, 6*MaxBodyBytes+4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(w, 413, "request body too large")
		} else {
			fail(w, 400, "invalid JSON: "+err.Error())
		}
		return false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		fail(w, 400, "request must contain exactly one JSON object")
		return false
	}
	return true
}

func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		fail(w, 404, err.Error())
	case errors.Is(err, ErrConflict):
		fail(w, 409, err.Error())
	default:
		log.Printf("database operation failed: %v", err)
		fail(w, 500, "database operation failed")
	}
}

func fail(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
