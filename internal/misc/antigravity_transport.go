package misc

import (
	"crypto/tls"
	"net/http"
)

func CloneAntigravityTransport(base *http.Transport) *http.Transport {
	if base == nil {
		base, _ = http.DefaultTransport.(*http.Transport)
	}
	if base == nil {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	return transport
}
