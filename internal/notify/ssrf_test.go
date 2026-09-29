package notify

import "testing"

func TestValidateEndpointHost(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{"api.example.com": true}

	tests := []struct {
		name    string
		url     string
		opts    clientOptions
		wantErr bool
	}{
		{"allowed https host", "https://api.example.com/path", clientOptions{}, false},
		{"disallowed host", "https://evil.example.net/path", clientOptions{}, true},
		{"http scheme rejected even for allowed host", "http://api.example.com/path", clientOptions{}, true},
		{"malformed url", "://not a url", clientOptions{}, true},
		{"custom endpoints allow any https host", "https://internal.example.org/path", clientOptions{allowCustomEndpoints: true}, false},
		{"custom endpoints allow http too (test-only mode)", "http://internal.example.org/path", clientOptions{allowCustomEndpoints: true}, false},
		{"empty host", "https:///path", clientOptions{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateEndpointHost(tt.url, allowed, tt.opts)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateEndpointHost(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestValidateEndpointHost_CaseInsensitiveHost(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{"api.example.com": true}
	if _, err := validateEndpointHost("https://API.EXAMPLE.COM/x", allowed, clientOptions{}); err != nil {
		t.Errorf("validateEndpointHost() with uppercase host = %v, want nil error", err)
	}
}
