package taskhub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestArchiveHidesTaskAndRestorePreservesContent(t *testing.T) {
	s, ts := fixture(t)
	original, err := s.Add(context.Background(), "Keep my document", "# Requirements\nDo not erase this.\n", "review", "ellie")
	if err != nil {
		t.Fatal(err)
	}
	code, raw := api(t, ts.URL, "PATCH", "/tasks/1", testToken, `{"archived":true}`)
	var archived Task
	if err := json.Unmarshal(raw, &archived); err != nil || code != 200 {
		t.Fatalf("archive: %d %s %v", code, raw, err)
	}
	want := original
	want.Archived = true
	if archived != want {
		t.Fatalf("archive changed content: %+v", archived)
	}
	for _, path := range []string{"/tasks/1", "/tasks/1?archived=false"} {
		code, raw := api(t, ts.URL, "GET", path, testToken, "")
		if code != 404 || strings.Contains(string(raw), original.Body) {
			t.Fatalf("archived task exposed: %d %s", code, raw)
		}
	}
	for _, path := range []string{"/tasks", "/tasks?project=ellie&status=review", "/tasks?archived=false"} {
		code, raw := api(t, ts.URL, "GET", path, testToken, "")
		if code != 200 || strings.TrimSpace(string(raw)) != "[]" {
			t.Fatalf("archived task listed: %d %s", code, raw)
		}
	}
	code, raw = api(t, ts.URL, "GET", "/tasks?archived=true&project=ellie&status=review", testToken, "")
	var summaries []Summary
	if err := json.Unmarshal(raw, &summaries); err != nil || code != 200 || len(summaries) != 1 || !summaries[0].Archived {
		t.Fatalf("archive list: %d %s %v", code, raw, err)
	}
	code, raw = api(t, ts.URL, "GET", "/tasks/1?archived=true", testToken, "")
	if err := json.Unmarshal(raw, &archived); err != nil || code != 200 || archived != want {
		t.Fatalf("explicit read: %d %s %v", code, raw, err)
	}
	for _, payload := range []string{`{"status":"done"}`, `{"project":"plate"}`, `{"body":"replacement","if_status":"review"}`} {
		code, raw = api(t, ts.URL, "PATCH", "/tasks/1", testToken, payload)
		if code != 404 {
			t.Fatalf("edited archived task: %d %s", code, raw)
		}
	}
	code, raw = api(t, ts.URL, "PATCH", "/tasks/1", testToken, `{"archived":true}`)
	if code != 200 {
		t.Fatalf("repeat archive: %d %s", code, raw)
	}
	code, raw = api(t, ts.URL, "PATCH", "/tasks/1", testToken, `{"archived":false}`)
	if err := json.Unmarshal(raw, &archived); err != nil || code != 200 || archived != original {
		t.Fatalf("restore changed content: %d %s %v", code, raw, err)
	}
	restored, err := s.Get(context.Background(), original.ID, false)
	if err != nil || restored != original {
		t.Fatalf("restored task unavailable: %+v %v", restored, err)
	}
}

func TestArchiveValidationAndAuthorization(t *testing.T) {
	s, ts := fixture(t)
	if _, err := s.Add(context.Background(), "A", "body", "pending", ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, token, payload string
		want                         int
	}{
		{"PATCH", "/tasks/1", "", `{"archived":true}`, 401},
		{"PATCH", "/tasks/1", testToken, `{"archived":true,"title":"changed"}`, 400},
		{"PATCH", "/tasks/1", testToken, `{"archived":"true"}`, 400},
		{"PATCH", "/tasks/1", testToken, `{"archived":true,"if_status":"done"}`, 409},
		{"PATCH", "/tasks/999", testToken, `{"archived":true}`, 404},
		{"PATCH", "/tasks/999", testToken, `{"archived":false}`, 404},
		{"GET", "/tasks?archived=invalid", testToken, "", 400},
		{"GET", "/tasks/1?archived=invalid", testToken, "", 400},
	} {
		code, raw := api(t, ts.URL, tc.method, tc.path, tc.token, tc.payload)
		if code != tc.want {
			t.Errorf("%s %s %s: %d %s", tc.method, tc.path, tc.payload, code, raw)
		}
	}
	task, err := s.Get(context.Background(), 1, false)
	if err != nil || task.Archived || task.Title != "A" {
		t.Fatalf("invalid request changed task: %+v %v", task, err)
	}
}
