package observability

import (
	"net"
	"net/http"
	"time"
)

// SharedTransport is a package-level shared HTTP transport used by LokiPusher
// and PromPusher to reuse TCP connections and TLS handshakes to Grafana Cloud.
var SharedTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	MaxIdleConns:          10,
	MaxIdleConnsPerHost:   5,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   5 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}
