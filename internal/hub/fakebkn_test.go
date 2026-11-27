package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/javimosch/agents-hub/internal/bkn"
)

// bknDefaultPage is what the real store returns when no limit is given
// (bkn internal/store/verbs.go). A fake without it cannot catch a caller that
// forgets to page.
const bknDefaultPage = 50

// fakeBKN is the subset of bkn's store API the hub uses. It speaks HTTP so
// the real bkn.Client is under test too.
type fakeBKN struct {
	mu   sync.Mutex
	next int
	docs map[string][]bkn.Record // "ns/coll" -> records in creation order
	// beforeGuarded, when set, changes a document just before a conditional
	// patch checks it: another writer winning the race.
	beforeGuarded func(map[string]any)
}

// newTestHub gives a test its own hub and store. Nothing is shared.
func newTestHub(t *testing.T) (*Hub, *fakeBKN) {
	t.Helper()
	f := &fakeBKN{docs: map[string][]bkn.Record{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(bkn.New(srv.URL, "test-token", 0)), f
}

// seed inserts a record directly, bypassing the code under test.
func (f *fakeBKN) seed(ref string, doc bkn.Record) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.insert(ref, doc)
}

// all returns every stored record, unpaged: the truth a test asserts against.
func (f *fakeBKN) all(ref string) []bkn.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bkn.Record(nil), f.docs[ref]...)
}

func (f *fakeBKN) insert(ref string, doc bkn.Record) string {
	f.next++
	id := fmt.Sprintf("id%06d", f.next)
	doc["id"] = id
	f.docs[ref] = append(f.docs[ref], doc)
	return id
}

func (f *fakeBKN) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/store/"), "/")
	if len(parts) < 2 {
		http.Error(w, "no route", http.StatusNotFound)
		return
	}
	ref := parts[0] + "/" + parts[1]
	var body bkn.Record
	if r.Method == http.MethodPost || r.Method == http.MethodPatch {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if len(parts) == 2 {
		if r.Method == http.MethodPost {
			f.insert(ref, body)
			reply(map[string]any{"record": body})
			return
		}
		reply(map[string]any{"records": f.list(ref, r)})
		return
	}
	for i, doc := range f.docs[ref] {
		if doc["id"] != parts[2] {
			continue
		}
		switch r.Method {
		case http.MethodPatch:
			// bkn server.go storePatch: ?if=field=value guards the write, and
			// because every hub collection is owner-scoped, a failed guard
			// answers 404 rather than 409.
			if f.beforeGuarded != nil && len(r.URL.Query()["if"]) > 0 {
				f.beforeGuarded(doc)
			}
			for _, cond := range r.URL.Query()["if"] {
				field, want, _ := strings.Cut(cond, "=")
				if got, ok := doc[field]; !ok || fmt.Sprint(got) != want {
					w.WriteHeader(http.StatusNotFound)
					reply(map[string]any{"error": map[string]any{"type": "not_found", "message": "record not found"}})
					return
				}
			}
			for k, v := range body {
				patchField(doc, k, v)
			}
		case http.MethodDelete:
			f.docs[ref] = append(f.docs[ref][:i], f.docs[ref][i+1:]...)
		}
		reply(map[string]any{"record": doc})
		return
	}
	w.WriteHeader(http.StatusNotFound)
	reply(map[string]any{"error": map[string]any{"type": "not_found", "message": "no record"}})
}

func patchField(doc bkn.Record, k string, v any) {
	if op, ok := v.(map[string]any); ok {
		if n, ok := op["$inc"].(float64); ok {
			doc[k] = doc.Num(k) + n
			return
		}
		if s, ok := op["$setIfEmpty"]; ok {
			if doc[k] == nil || doc[k] == "" {
				doc[k] = s
			}
			return
		}
	}
	doc[k] = v
}

// list mirrors bkn: equality filters, descending unless order=asc, and a
// default page of 50.
func (f *fakeBKN) list(ref string, r *http.Request) []bkn.Record {
	q := r.URL.Query()
	out := []bkn.Record{}
next:
	for _, doc := range f.docs[ref] {
		for k := range q {
			switch k {
			case "limit", "offset", "order_by", "order":
				continue
			}
			if fmt.Sprint(doc[k]) != q.Get(k) {
				continue next
			}
		}
		out = append(out, doc)
	}
	by, desc := q.Get("order_by"), q.Get("order") != "asc"
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if desc {
			a, b = b, a
		}
		if x, ok := a[by].(float64); ok {
			return x < b.Num(by)
		}
		return a.Str(by) < b.Str(by)
	})
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = bknDefaultPage
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	out = out[min(offset, len(out)):]
	return out[:min(limit, len(out))]
}
