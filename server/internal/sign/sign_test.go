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

func TestDisabledIsNoop(t *testing.T) {
	New("").Set("red", "A") // must not panic or block
	var c *Client
	c.Set("red", "A")
}
