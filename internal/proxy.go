package internal

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync/atomic"

	"go.uber.org/zap"
)

const (
	ConnectNormal = iota
	ConnectMitm
)

type ProxyHttpServer struct {
	Sess              int64
	NonSupportHandler http.Handler
	Tr                *http.Transport
	ProxyType         int
}

var hasPort = regexp.MustCompile(`:\d+$`)

func (proxy *ProxyHttpServer) removeProxyRelatedHeaders(r *http.Request) {
	// If no Accept-Encoding header exists, Transport will add the headers it can accept
	// and would wrap the response body with the relevant reader.
	r.Header.Del("Accept-Encoding")
	// curl can add that, see
	// https://jdebp.eu./FGA/web-proxy-connection-header.html
	r.Header.Del("Proxy-Connection")
	r.Header.Del("Proxy-Authenticate")
	r.Header.Del("Proxy-Authorization")
	// Connection, Authenticate and Authorization are single hop Header:
	// http://www.w3.org/Protocols/rfc2616/rfc2616.txt
	// 14.10 Connection
	//   The Connection general-header field allows the sender to specify
	//   options that are desired for that particular connection and MUST NOT
	//   be communicated by proxies over further connections.
	r.Header.Del("Connection")
}

func (proxy *ProxyHttpServer) copyHeaders(dst, src http.Header) {
	for k, vlist := range src {
		for _, v := range vlist {
			dst.Add(k, v)
		}
	}
}

func (proxy *ProxyHttpServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := ProxyCtx{Req: r, Sess: atomic.AddInt64(&proxy.Sess, 1)}

	if r.Method == "CONNECT" {
		proxy.handleHttps(w, r, &ctx)
	} else {
		zap.S().Infof("[Session: %v] Got request: %v, %v, %v, %v", ctx.Sess, r.Method, r.Host, r.URL.Path, r.URL.String())

		if !r.URL.IsAbs() {
			proxy.NonSupportHandler.ServeHTTP(w, r)
			return
		}

		proxy.removeProxyRelatedHeaders(r)
		resp, err := proxy.Tr.RoundTrip(r)
		if err != nil || resp == nil {
			errorString := fmt.Sprintf("Received error status: %v", err.Error())
			zap.S().Errorf("[Session: %v] The error is: ", ctx.Sess, errorString)
			http.Error(w, errorString, 500)
			return
		}

		defer resp.Body.Close()
		proxy.copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		nr, err := io.Copy(w, resp.Body)
		if err != nil {
			zap.S().Errorf("[Session: %v] Error copying data from remote to client.", ctx.Sess)
		}
		zap.S().Infof("[Session: %v] Deliver %v bytes from remote to client", ctx.Sess, nr)
	}
}

func NewProxyHttpServer() *ProxyHttpServer {
	proxy := ProxyHttpServer{
		NonSupportHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "[Session: %v] This is a proxy server, your request cannot be recognized.", 500)
		}),
		Tr: &http.Transport{},
	}
	return &proxy
}
