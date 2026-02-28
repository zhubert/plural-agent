package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// apiRequest performs an HTTP request with common boilerplate shared by the
// Asana and Linear providers: build the request, set auth/content headers,
// execute, check for 403 and unexpected status codes, and optionally JSON-
// decode the response body.
//
// Parameters:
//   - ctx: request context
//   - client: HTTP client to use
//   - method: HTTP method (GET, POST, etc.)
//   - url: fully-formed request URL
//   - body: request body (may be nil for GET requests)
//   - authHeader: value for the Authorization header (e.g. "Bearer <token>" or raw key)
//   - expectStatus: expected success status code (e.g. http.StatusOK, http.StatusCreated)
//   - forbiddenMsg: if non-empty, returned as the error message on 403; if empty, 403 falls through to generic status check
//   - providerName: name used in generic error messages (e.g. "Asana", "Linear")
//   - result: target for JSON decoding (may be nil to skip decoding)
func apiRequest(ctx context.Context, client *http.Client, method, url string, body io.Reader, authHeader string, expectStatus int, forbiddenMsg, providerName string, result any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", authHeader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s API request failed: %w", providerName, err)
	}
	defer resp.Body.Close()

	if forbiddenMsg != "" && resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%s", forbiddenMsg)
	}

	if resp.StatusCode != expectStatus {
		return fmt.Errorf("%s API returned status %d", providerName, resp.StatusCode)
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("failed to parse %s response: %w", providerName, err)
		}
	}

	return nil
}
