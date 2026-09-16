package taskhub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testToken = "test-only-token-1234567890"

func api(t *testing.T, server, method, path, token, body string) (int, []byte) {
	t.Helper()
	r, err := http.NewRequest(method, server+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, b
}

func fixture(t *testing.T) (*Store, *httptest.Server) {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h, err := Handler(s, testToken, "test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return s, ts
}

func TestTaskLifecycleAcrossClientsAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := Handler(s, testToken, "test")
	ts := httptest.NewServer(h)
	body := "# 需求\n\n实现登录。\n```sh\necho '$HOME'\n```\n"
	payload, _ := json.Marshal(map[string]string{"title": " 登录 ", "body": body})
	code, raw := api(t, ts.URL, "POST", "/tasks", testToken, string(payload))
	if code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	var task Task
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	if task.ID != 1 || task.Body != body || task.Title != "登录" || task.Status != "pending" {
		t.Fatalf("unexpected task: %+v", task)
	}
	code, raw = api(t, ts.URL, "PATCH", "/tasks/1", testToken, `{"status":"in_progress","if_status":"pending"}`)
	if code != 200 {
		t.Fatalf("claim: %d %s", code, raw)
	}
	code, _ = api(t, ts.URL, "PATCH", "/tasks/1", testToken, `{"status":"in_progress","if_status":"pending"}`)
	if code != 409 {
		t.Fatalf("duplicate claim: %d", code)
	}
	for _, status := range []string{"review", "pending", "in_progress", "review", "done"} {
		code, raw = api(t, ts.URL, "PATCH", "/tasks/1", testToken, fmt.Sprintf(`{"status":%q}`, status))
		if code != 200 {
			t.Fatalf("transition: %d %s", code, raw)
		}
	}
	ts.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task, err = s.Get(context.Background(), 1)
	if err != nil || task.Body != body || task.Status != "done" {
		t.Fatalf("restart lost data: %+v %v", task, err)
	}
}

func TestConcurrentClaims(t *testing.T) {
	s, ts := fixture(t)
	_, err := s.Add(context.Background(), "one job", "", "pending")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan int, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := api(t, ts.URL, "PATCH", "/tasks/1", testToken, `{"status":"in_progress","if_status":"pending"}`)
			results <- code
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for code := range results {
		if code == 200 {
			winners++
		} else if code != 409 {
			t.Fatalf("unexpected code: %d", code)
		}
	}
	if winners != 1 {
		t.Fatalf("got %d claim winners", winners)
	}
}

func TestAuthValidationAndMissingTasks(t *testing.T) {
	_, ts := fixture(t)
	cases := []struct {
		method, path, token, body string
		code                      int
	}{
		{"GET", "/healthz", "", "", 200},
		{"GET", "/tasks", "", "", 401},
		{"POST", "/tasks", "wrong", `{"title":"x"}`, 401},
		{"POST", "/tasks", testToken, `{"title":"x","extra":true}`, 400},
		{"POST", "/tasks", testToken, `{"title":"x"} {}`, 400},
		{"POST", "/tasks", testToken, `{"title":"  "}`, 400},
		{"POST", "/tasks", testToken, `{"title":"x","status":"bogus"}`, 400},
		{"PATCH", "/tasks/1", testToken, `{}`, 400},
		{"PATCH", "/tasks/1", testToken, `{"status":"done"}`, 404},
		{"PATCH", "/tasks/1", testToken, `{"status":"done","if_status":"pending"}`, 404},
		{"GET", "/tasks/0", testToken, "", 400},
		{"GET", "/tasks/999", testToken, "", 404},
		{"GET", "/tasks?status=bogus", testToken, "", 400},
		{"GET", "/tasks?limit=0", testToken, "", 400},
		{"GET", "/tasks?after=-1", testToken, "", 400},
	}
	for _, c := range cases {
		code, raw := api(t, ts.URL, c.method, c.path, c.token, c.body)
		if code != c.code {
			t.Errorf("%s %s %s: got %d want %d: %s", c.method, c.path, c.body, code, c.code, raw)
		}
	}
	payload, _ := json.Marshal(map[string]string{"title": "large", "body": strings.Repeat("x", MaxBodyBytes+1)})
	code, _ := api(t, ts.URL, "POST", "/tasks", testToken, string(payload))
	if code != 400 {
		t.Fatalf("oversized body: %d", code)
	}
	if h, err := Handler(nil, "", "test"); err == nil || h != nil {
		t.Fatal("empty server token accepted")
	}
}

func TestFilteringPaginationAndPartialUpdate(t *testing.T) {
	s, ts := fixture(t)
	for i := 0; i < 3; i++ {
		if _, err := s.Add(context.Background(), "title", "keep this body", "pending"); err != nil {
			t.Fatal(err)
		}
	}
	code, raw := api(t, ts.URL, "PATCH", "/tasks/2", testToken, `{"title":"new title","status":"review"}`)
	if code != 200 {
		t.Fatalf("update: %d %s", code, raw)
	}
	var task Task
	json.Unmarshal(raw, &task)
	if task.Body != "keep this body" {
		t.Fatal("partial update erased body")
	}
	code, raw = api(t, ts.URL, "GET", "/tasks?status=pending&after=1&limit=1", testToken, "")
	var list []Summary
	json.Unmarshal(raw, &list)
	if code != 200 || len(list) != 1 || list[0].ID != 3 {
		t.Fatalf("pagination: %d %s", code, raw)
	}
	if bytes.Contains(raw, []byte("body")) {
		t.Fatal("list exposed full body")
	}
	_, raw = api(t, ts.URL, "GET", "/tasks?status=done", testToken, "")
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("empty list: %s", raw)
	}
	code, raw = api(t, ts.URL, "PATCH", "/tasks/2", testToken, `{"body":""}`)
	json.Unmarshal(raw, &task)
	if code != 200 || task.Body != "" {
		t.Fatalf("clear body: %d %s", code, raw)
	}
}
