package main

// Tier-1 tests for the apiCall/apiGet HTTP helpers in secrets.go.
// These bypass the SSH tunnel entirely: they construct a *connection that
// points directly at an httptest.Server, verifying auth headers, error
// handling, and correct body propagation.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeConn builds a *connection aimed at ts with the given token.
func fakeConn(ts *httptest.Server, token string) *connection {
	return &connection{baseURL: ts.URL, token: token}
}

func TestAPICall_SendsBearerAuthHeader(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	_, err := apiCall(fakeConn(ts, "my-secret"), http.MethodGet, "/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer my-secret" {
		t.Errorf("Authorization = %q, want \"Bearer my-secret\"", gotAuth)
	}
}

func TestAPICall_ReturnsBodyOnSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer ts.Close()

	body, err := apiCall(fakeConn(ts, "tok"), http.MethodGet, "/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"result":"ok"}` {
		t.Errorf("body = %s", body)
	}
}

func TestAPICall_Returns401AsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	_, err := apiCall(fakeConn(ts, "bad-token"), http.MethodGet, "/test", nil)
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

func TestAPICall_Returns404AsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("service not found"))
	}))
	defer ts.Close()

	_, err := apiCall(fakeConn(ts, "tok"), http.MethodGet, "/missing", nil)
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
}

func TestAPICall_Returns500AsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer ts.Close()

	_, err := apiCall(fakeConn(ts, "tok"), http.MethodGet, "/boom", nil)
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}

func TestAPICall_PostSendsJSONContentTypeAndBody(t *testing.T) {
	var gotContentType string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	_, err := apiCall(fakeConn(ts, "tok"), http.MethodPost, "/test", []byte(`{"k":"v"}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if string(gotBody) != `{"k":"v"}` {
		t.Errorf("body = %s, want {\"k\":\"v\"}", gotBody)
	}
}

func TestAPICall_GetDoesNotSendContentType(t *testing.T) {
	var gotContentType string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	_, _ = apiCall(fakeConn(ts, "tok"), http.MethodGet, "/test", nil)
	if gotContentType != "" {
		t.Errorf("Content-Type = %q, want empty for GET with no body", gotContentType)
	}
}
