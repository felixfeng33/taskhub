package taskhub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	task, err = s.Get(context.Background(), 1, false)
	if err != nil || task.Body != body || task.Status != "done" {
		t.Fatalf("restart lost data: %+v %v", task, err)
	}
}

func TestConcurrentClaims(t *testing.T) {
	s, ts := fixture(t)
	_, err := s.Add(context.Background(), "one job", "", "pending", "")
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
		if _, err := s.Add(context.Background(), "title", "keep this body", "pending", ""); err != nil {
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

func TestUpgradeFromV010PreservesTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE tasks (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, body TEXT NOT NULL DEFAULT '', status TEXT NOT NULL);
INSERT INTO tasks(id,title,body,status) VALUES(7,'Existing task','原来的正文','in_progress');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	task, err := s.Get(ctx, 7, false)
	if err != nil || task != (Task{ID: 7, Title: "Existing task", Body: "原来的正文", Status: "in_progress"}) {
		t.Fatalf("migration changed task: %+v %v", task, err)
	}
	project := "ellie"
	if _, err := s.Update(ctx, 7, Update{Project: &project}); err != nil {
		t.Fatal(err)
	}
	archive := true
	if _, err := s.Update(ctx, 7, Update{Archived: &archive}); err != nil {
		t.Fatal(err)
	}
	// A v0.1.0-style writer omits the added column; its new tasks stay valid.
	if _, err := s.db.Exec(`INSERT INTO tasks(title,body,status) VALUES('Legacy writer','','pending')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Get(ctx, 7, false); err != ErrNotFound {
		t.Fatalf("reopening revealed archived task: %v", err)
	}
	task, err = s.Get(ctx, 7, true)
	if err != nil || !task.Archived || task.Project != "ellie" || task.Body != "原来的正文" || task.Status != "in_progress" {
		t.Fatalf("reopen changed task: %+v %v", task, err)
	}
	legacy, err := s.Get(ctx, 8, false)
	if err != nil || legacy.Project != "" {
		t.Fatalf("old writer compatibility: %+v %v", legacy, err)
	}
}

func TestProjectsAcrossCreateUpdateAndList(t *testing.T) {
	_, ts := fixture(t)
	for _, body := range []string{
		`{"title":"A","body":"preserve A","project":" ellie "}`,
		`{"title":"B","body":"preserve B","project":"plate"}`,
		`{"title":"C"}`,
		`{"title":"D","project":"ellie","status":"review"}`,
	} {
		code, raw := api(t, ts.URL, "POST", "/tasks", testToken, body)
		if code != 201 {
			t.Fatalf("create: %d %s", code, raw)
		}
	}
	for _, tc := range []struct {
		query string
		ids   []int64
	}{
		{"", []int64{1, 2, 3, 4}},
		{"?project=ellie", []int64{1, 4}},
		{"?project=ellie&status=pending", []int64{1}},
		{"?project=ellie&after=1&limit=1", []int64{4}},
		{"?project=", []int64{3}},
		{"?project=Ellie", nil},
		{"?project=unknown", nil},
	} {
		code, raw := api(t, ts.URL, "GET", "/tasks"+tc.query, testToken, "")
		var tasks []Summary
		if err := json.Unmarshal(raw, &tasks); err != nil || code != 200 {
			t.Fatalf("list: %d %s %v", code, raw, err)
		}
		if len(tasks) != len(tc.ids) {
			t.Fatalf("%s: %s", tc.query, raw)
		}
		for i, task := range tasks {
			if task.ID != tc.ids[i] {
				t.Fatalf("%s: %s", tc.query, raw)
			}
		}
	}
	code, raw := api(t, ts.URL, "PATCH", "/tasks/2", testToken, `{"project":" 中文项目 "}`)
	var task Task
	json.Unmarshal(raw, &task)
	if code != 200 || task.Project != "中文项目" || task.Body != "preserve B" || task.Status != "pending" {
		t.Fatalf("project-only update: %d %s", code, raw)
	}
	code, raw = api(t, ts.URL, "GET", "/tasks?project="+url.QueryEscape("中文项目"), testToken, "")
	var summaries []Summary
	json.Unmarshal(raw, &summaries)
	if code != 200 || len(summaries) != 1 || summaries[0].ID != 2 {
		t.Fatalf("Unicode filter: %d %s", code, raw)
	}
	code, raw = api(t, ts.URL, "PATCH", "/tasks/2", testToken, `{"status":"review"}`)
	json.Unmarshal(raw, &task)
	if code != 200 || task.Project != "中文项目" {
		t.Fatalf("legacy update erased project: %d %s", code, raw)
	}
	code, raw = api(t, ts.URL, "PATCH", "/tasks/2", testToken, `{"project":""}`)
	json.Unmarshal(raw, &task)
	if code != 200 || task.Project != "" || task.Body != "preserve B" {
		t.Fatalf("clear project: %d %s", code, raw)
	}
	for _, project := range []string{strings.Repeat("界", 81), "a\nb", "a\tb", "a\x1bb"} {
		payload, _ := json.Marshal(map[string]string{"title": "bad", "project": project})
		code, _ := api(t, ts.URL, "POST", "/tasks", testToken, string(payload))
		if code != 400 {
			t.Errorf("create accepted invalid project %q: %d", project, code)
		}
		code, _ = api(t, ts.URL, "PATCH", "/tasks/1", testToken, string(payload))
		if code != 400 {
			t.Errorf("update accepted invalid project %q: %d", project, code)
		}
		code, _ = api(t, ts.URL, "GET", "/tasks?project="+url.QueryEscape(project), testToken, "")
		if code != 400 {
			t.Errorf("filter accepted invalid project %q: %d", project, code)
		}
	}
	if err := ValidateProject(strings.Repeat("界", 80)); err != nil {
		t.Fatalf("80 Unicode characters rejected: %v", err)
	}
}
