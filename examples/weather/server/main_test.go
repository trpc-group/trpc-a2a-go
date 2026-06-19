package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetWeather is the unit test for the getWeather function.
func TestGetWeather(t *testing.T) {
	// 1. Create an HTTP mock server.
	// httptest.NewServer starts a local server that only runs during the test.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 2. Define the server's behavior.
		// Check if the request URL is what we expect.
		if r.URL.Path == "/Beijing" {
			// If it is, return a predefined successful response.
			w.WriteHeader(http.StatusOK)    // Set status code to 200
			fmt.Fprintln(w, "Beijing: ☀️ +25°C") // Write the response body
		} else {
			// Otherwise, return 404 Not Found.
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	// 'defer' ensures the mock server is closed after the test finishes.
	defer server.Close()

	// 3. Run our getWeather function, but point the API address to the mock server.
	weather, err := getWeather("Beijing", server.URL) // Pass the mock server's URL

	// 4. Assert the results.
	if err != nil {
		t.Errorf("getWeather() error = %v, wantErr nil", err)
	}

	expectedWeather := "Beijing: ☀️ +25°C\n" // Fprintln adds a newline character
	if weather != expectedWeather {
		t.Errorf("getWeather() = %v, want %v", weather, expectedWeather)
	}
}