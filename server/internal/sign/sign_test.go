package sign

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSetSendsLevelOnce(t *testing.T) {
	got := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.URL.Path + "?" + r.URL.RawQuery
	}))
	defer srv.Close()
	c := New(srv.URL + "/")
	c.Set("red", "B")
	c.Set("red", "B") // repeat: skipped
	select {
	case q := <-got:
		if q != "/level?v=red&zone=B" {
			t.Fatalf("got %s", q)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sign not called")
	}
	select {
	case q := <-got:
		t.Fatalf("repeat was sent: %s", q)
	case <-time.After(200 * time.Millisecond):
	}
}

// The Uno R4 sometimes resets a connection that arrives while it is still
// closing the previous one; a one-off test alert must still get through.
func TestRetriesAResetConnection(t *testing.T) {
	retryDelay = 10 * time.Millisecond
	calls := 0
	got := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close() // drop it like the board does
			return
		}
		got <- r.URL.RawQuery
	}))
	defer srv.Close()
	New(srv.URL).Force("red", "B")
	select {
	case q := <-got:
		if q != "v=red&zone=B" {
			t.Fatalf("got %s", q)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no retry after a reset connection")
	}
}

func TestDisabledIsNoop(t *testing.T) {
	New("").Set("red", "A") // must not panic or block
	var c *Client
	c.Set("red", "A")
}

func TestZoneSigns(t *testing.T) {
	type hit struct{ who, q string }
	got := make(chan hit, 20)
	mk := func(who string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got <- hit{who, r.URL.RawQuery}
		}))
	}
	main, a, b := mk("main"), mk("A"), mk("B")
	defer main.Close()
	defer a.Close()
	defer b.Close()
	c := New(main.URL + ", a=" + a.URL + ",B=" + b.URL)
	c.Update(map[string]string{"A": "calm", "B": "red"}, "red", "B")
	want := map[hit]bool{{"main", "v=red&zone=B"}: true, {"A", "v=calm&zone=A"}: true, {"B", "v=red&zone=B"}: true}
	for range 3 {
		select {
		case h := <-got:
			if !want[h] {
				t.Errorf("unexpected %+v", h)
			}
			delete(want, h)
		case <-time.After(2 * time.Second):
			t.Fatalf("missing %v", want)
		}
	}
}
