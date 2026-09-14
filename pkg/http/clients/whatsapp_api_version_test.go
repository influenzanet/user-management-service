package clients

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// urlCapturingTransport answers every request with a success body and records the URL, so a
// test can see the full Graph URL without pointing the client at a local server.
type urlCapturingTransport struct{ url string }

func (t *urlCapturingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.url = r.URL.String()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"messages":[{"id":"wamid.test"}]}`)),
		Request:    r,
	}, nil
}

func TestSendVerificationCodeCallsTheConfiguredVersion(t *testing.T) {
	c := NewWhatsAppClient("token", "phone-id", "verify_code", "v25.0")
	transport := &urlCapturingTransport{}
	c.httpClient.Transport = transport
	if err := c.SendVerificationCode(context.Background(), "+391234567890", "123456", "it"); err != nil {
		t.Fatalf("SendVerificationCode: %v", err)
	}
	if transport.url != "https://graph.facebook.com/v25.0/phone-id/messages" {
		t.Fatalf("request URL = %q, want the configured version in the Graph URL", transport.url)
	}
}

func TestResolveAPIVersion(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		want       string
	}{
		{"unset uses the default", "", "v26.0"},
		{"well-formed value is used as is", "v25.0", "v25.0"},
		{"missing v prefix falls back to the default", "26.0", "v26.0"},
		{"missing minor falls back to the default", "v26", "v26.0"},
		{"upper-case prefix falls back to the default", "V26.0", "v26.0"},
		{"leading space falls back to the default", " v26.0", "v26.0"},
		{"trailing slash falls back to the default", "v26.0/", "v26.0"},
		{"free text falls back to the default", "latest", "v26.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveAPIVersion(tc.configured); got != tc.want {
				t.Fatalf("ResolveAPIVersion(%q) = %q, want %q", tc.configured, got, tc.want)
			}
		})
	}
}

func TestNewWhatsAppClientBuildsBaseURLFromAPIVersion(t *testing.T) {
	c := NewWhatsAppClient("token", "phone-id", "verify_code", "v25.0")
	if c.apiBaseURL != "https://graph.facebook.com/v25.0" {
		t.Fatalf("apiBaseURL = %q, want the configured version", c.apiBaseURL)
	}
	c = NewWhatsAppClient("token", "phone-id", "verify_code", "")
	if c.apiBaseURL != "https://graph.facebook.com/v26.0" {
		t.Fatalf("apiBaseURL = %q, want the default version", c.apiBaseURL)
	}
}

// The three send methods must build their URL from the client's base URL, not from a
// package constant, otherwise the configured version is silently ignored.
func TestSendMethodsUseTheClientBaseURL(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.test"}]}`))
	}))
	t.Cleanup(server.Close)

	c := NewWhatsAppClient("token", "phone-id", "verify_code", "v26.0")
	c.apiBaseURL = server.URL
	ctx := context.Background()
	if err := c.SendVerificationCode(ctx, "+391234567890", "123456", "it"); err != nil {
		t.Fatalf("SendVerificationCode: %v", err)
	}
	if err := c.SendTextMessage(ctx, "+391234567890", "hello"); err != nil {
		t.Fatalf("SendTextMessage: %v", err)
	}
	if err := c.SendTemplateMessage(ctx, "+391234567890", "weekly", "it", map[string]string{}); err != nil {
		t.Fatalf("SendTemplateMessage: %v", err)
	}
	if len(paths) != 3 {
		t.Fatalf("expected 3 requests against the test server, got %d", len(paths))
	}
	for _, p := range paths {
		if p != "/phone-id/messages" {
			t.Errorf("request path = %q, want /phone-id/messages", p)
		}
	}
}
