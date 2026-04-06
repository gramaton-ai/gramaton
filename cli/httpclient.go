package cli

import (
	"bytes"
	"net/http"
	"time"
)

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// healthClient has a short timeout for server discovery health checks.
// Prevents long hangs when connecting to stale/dead server info.
var healthClient = &http.Client{
	Timeout: 3 * time.Second,
}

// httpGet sends a GET request.
func httpGet(url string) (*http.Response, error) {
	return httpClient.Get(url)
}

// httpPost sends a POST request with an optional JSON body.
func httpPost(url string, body []byte) (*http.Response, error) {
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	return httpClient.Post(url, "application/json", reader)
}
