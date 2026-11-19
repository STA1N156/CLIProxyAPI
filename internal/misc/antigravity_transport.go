package misc

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	utls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/httpwire"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"golang.org/x/net/proxy"
)

func CloneAntigravityTransport(base *http.Transport) *http.Transport {
	if base == nil {
		base, _ = http.DefaultTransport.(*http.Transport)
	}
	if base == nil {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.ForceAttemptHTTP2 = true
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.NextProtos = []string{"h2", "http/1.1"}
	// Keep explicitly supplied TLS dialers, including already configured clones.
	if base.DialTLSContext != nil {
		return transport
	}
	dialContext, proxyForRequest := transport.DialContext, transport.Proxy
	// Tunnel proxies before TLS so the upstream always sees the same profile.
	transport.Proxy = nil
	dial := func(ctx context.Context, network, address, scheme string) (net.Conn, error) {
		if proxyForRequest != nil {
			proxyURL, err := proxyForRequest((&http.Request{URL: &url.URL{Scheme: scheme, Host: address}}).WithContext(ctx))
			if err != nil {
				return nil, err
			}
			if proxyURL != nil {
				dialer, _, err := proxyutil.BuildDialer(proxyURL.String())
				if err != nil {
					return nil, err
				}
				if contextual, ok := dialer.(proxy.ContextDialer); ok {
					return contextual.DialContext(ctx, network, address)
				}
				return nil, fmt.Errorf("antigravity: proxy dialer does not support context")
			}
		}
		if dialContext != nil {
			return dialContext(ctx, network, address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address, "http")
		if err != nil {
			return nil, err
		}
		return httpwire.NewOrderedRequestConn(conn, antigravityHeaderOrder), nil
	}
	transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address, "https")
		if err != nil {
			return nil, err
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		baseConfig := transport.TLSClientConfig
		if baseConfig.ServerName != "" {
			host = baseConfig.ServerName
		}
		config := &utls.Config{ServerName: host, NextProtos: baseConfig.NextProtos, RootCAs: baseConfig.RootCAs, InsecureSkipVerify: baseConfig.InsecureSkipVerify, VerifyPeerCertificate: baseConfig.VerifyPeerCertificate}
		if verify := baseConfig.VerifyConnection; verify != nil {
			config.VerifyConnection = func(state utls.ConnectionState) error {
				return verify(tls.ConnectionState{Version: state.Version, HandshakeComplete: state.HandshakeComplete, DidResume: state.DidResume, CipherSuite: state.CipherSuite, NegotiatedProtocol: state.NegotiatedProtocol, ServerName: state.ServerName, PeerCertificates: state.PeerCertificates, VerifiedChains: state.VerifiedChains})
			}
		}
		secured := utls.UClient(conn, config, utls.HelloCustom)
		if err = secured.ApplyPreset(antigravityGoDefaultHello()); err == nil {
			err = secured.HandshakeContext(ctx)
		}
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("antigravity TLS: %w", err)
		}
		if secured.ConnectionState().NegotiatedProtocol == "h2" {
			return &officialTLSConn{UConn: secured}, nil
		}
		return httpwire.NewOrderedRequestConn(secured, antigravityHeaderOrder), nil
	}
	return transport
}

type officialTLSConn struct {
	*utls.UConn
}

func (c *officialTLSConn) ConnectionState() tls.ConnectionState {
	state := c.UConn.ConnectionState()
	return tls.ConnectionState{
		Version:                     state.Version,
		HandshakeComplete:           state.HandshakeComplete,
		DidResume:                   state.DidResume,
		CipherSuite:                 state.CipherSuite,
		NegotiatedProtocol:          state.NegotiatedProtocol,
		NegotiatedProtocolIsMutual:  state.NegotiatedProtocolIsMutual,
		ServerName:                  state.ServerName,
		PeerCertificates:            state.PeerCertificates,
		VerifiedChains:              state.VerifiedChains,
		SignedCertificateTimestamps: state.SignedCertificateTimestamps,
		OCSPResponse:                state.OCSPResponse,
		TLSUnique:                   state.TLSUnique,
		ECHAccepted:                 state.ECHAccepted,
	}
}

var antigravityHeaders = []string{"Host", "Accept", "Connection", "User-Agent", "Authorization", "Content-Type", "Accept-Encoding", "Content-Length"}
var antigravityStreamHeaders = []string{"Host", "User-Agent", "Transfer-Encoding", "Authorization", "Content-Type", "Accept-Encoding"}
var antigravityOAuthHeaders = []string{"Host", "Accept", "Accept-Encoding", "Connection", "User-Agent", "Content-Type", "Authorization", "Content-Length"}

func antigravityHeaderOrder(_, target string) []string {
	if strings.HasPrefix(target, "/token") || strings.HasPrefix(target, "/oauth2/v2/userinfo") {
		return antigravityOAuthHeaders
	}
	if strings.Contains(target, ":streamGenerateContent") {
		return antigravityStreamHeaders
	}
	return antigravityHeaders
}

// TLS profile supplied by the handoff package for language_server 2.31.0.
func antigravityGoDefaultHello() *utls.ClientHelloSpec {
	signatures := []utls.SignatureScheme{
		2308, 2309, 2310, 2052, 1027, 2055, 2053, 2054, 1025, 1281, 1537, 1283, 1539,
	}
	return &utls.ClientHelloSpec{
		TLSVersMin: utls.VersionTLS12, TLSVersMax: utls.VersionTLS13,
		CipherSuites:       []uint16{49195, 49199, 49196, 49200, 52393, 52392, 49161, 49171, 49162, 49172, 4865, 4866, 4867},
		CompressionMethods: []byte{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},
			&utls.GenericExtension{Id: 11, Data: []byte{1, 0}},
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
			&utls.ExtendedMasterSecretExtension{},
			&utls.SCTExtension{},
			&utls.GenericExtension{Id: 5, Data: []byte{1, 0, 0, 0, 0}},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{utls.X25519MLKEM768, 4587, 4589, 29, 23, 24, 25}},
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: signatures},
			&utls.SignatureAlgorithmsCertExtension{SupportedSignatureAlgorithms: append(signatures, 513, 515)},
			&utls.ALPNExtension{AlpnProtocols: []string{"h2", "http/1.1"}},
			&utls.SupportedVersionsExtension{Versions: []uint16{utls.VersionTLS13, utls.VersionTLS12}},
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{{Group: utls.X25519MLKEM768}, {Group: utls.X25519}}},
		},
	}
}
