package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithRecover_TurnsPanicInto500(t *testing.T) {
	panicking := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	handler := withRecover(testLogger())(panicking)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	// The whole point of withRecover is that this call must not panic
	// past ServeHTTP and crash the test (in production: crash the
	// listener goroutine).
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	var body errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Error != "internal server error" {
		t.Errorf("error message = %q, want the generic public message, not the panic detail", body.Error)
	}
}

func TestWithRecover_PassesThroughNormalResponses(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler := withRecover(testLogger())(ok)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
}

func TestWithRequestID_StoresIDInContext(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestIDFromContext(r.Context())
	})
	handler := withRequestID(next)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if seen == "" {
		t.Fatal("expected a request ID to be available via context inside the handler")
	}
	if seen != rec.Header().Get("X-Request-ID") {
		t.Errorf("context request ID %q does not match response header %q", seen, rec.Header().Get("X-Request-ID"))
	}
}

func TestChain_RunsMiddlewareInDeclaredOrder(t *testing.T) {
	var order []string
	mark := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
	})

	handler := chain(base, mark("A"), mark("B"), mark("C"))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	want := []string{"A", "B", "C", "handler"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestStatusRecorder_DefaultsTo200OnImplicitWrite(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	_, _ = rec.Write([]byte("hello"))

	if rec.status != http.StatusOK {
		t.Errorf("status = %d, want %d (implicit 200 per net/http semantics)", rec.status, http.StatusOK)
	}
}

func TestStatusRecorder_CapturesExplicitStatus(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	rec.WriteHeader(http.StatusAccepted)

	if rec.status != http.StatusAccepted {
		t.Errorf("status = %d, want %d", rec.status, http.StatusAccepted)
	}
}
