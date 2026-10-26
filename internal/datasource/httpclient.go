package datasource

import (
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/utils"
)

// ValidateConnectorBaseURL checks a connector API base URL against the SSRF policy.
// Empty rawURL is allowed; callers apply their own default before issuing requests.
func ValidateConnectorBaseURL(rawURL string) error {
	url := strings.TrimSpace(rawURL)
	if url == "" {
		return nil
	}
	if !strings.Contains(url, "://") {
		url = "https://" + url
	}
	if err := utils.ValidateURLForSSRF(url); err != nil {
		return fmt.Errorf("base_url SSRF validation failed: %w", err)
	}
	return nil
}

// ConnectorBaseURLForLog keeps only the scheme and host of a connector base URL.
// ValidateConnectorBaseURL accepts userinfo and a query, so a configured base_url
// may carry a credential that must not reach the log.
func ConnectorBaseURLForLog(rawURL string) string {
	u, err := neturl.Parse(rawURL)
	if err != nil || u.Host == "" {
		return fmt.Sprintf("(unparsed len=%d)", len(rawURL))
	}
	return u.Scheme + "://" + u.Host
}

// NewConnectorHTTPClient returns an HTTP client with redirect and dial-time SSRF guards.
func NewConnectorHTTPClient(timeout time.Duration) *http.Client {
	cfg := utils.DefaultSSRFSafeHTTPClientConfig()
	cfg.Timeout = timeout
	return utils.NewSSRFSafeHTTPClient(cfg)
}
