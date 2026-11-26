// Agent24 fork product patch A24-KB-62 (KB-LITE-02a, G5): Host/Origin allowlist on the loopback port.

package agent24managed

import (
	"net/http"
	"strconv"
	"strings"
)

// LoopbackGuard answers 403 (empty body) unless the request's Host is exactly 127.0.0.1:<port> or
// localhost:<port> (case-insensitive host part), and, when an Origin header is present, it is exactly
// http://127.0.0.1:<port> or http://localhost:<port>. This stops DNS rebinding: a page on
// evil.example that resolves to 127.0.0.1 still sends Host: evil.example.
func LoopbackGuard(next http.Handler, port int) http.Handler {
	suffix := ":" + strconv.Itoa(port)
	hosts := map[string]struct{}{"127.0.0.1" + suffix: {}, "localhost" + suffix: {}}
	origins := map[string]struct{}{"http://127.0.0.1" + suffix: {}, "http://localhost" + suffix: {}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := hosts[strings.ToLower(r.Host)]; !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if values, present := r.Header["Origin"]; present {
			if len(values) != 1 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if _, ok := origins[strings.ToLower(values[0])]; !ok {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
