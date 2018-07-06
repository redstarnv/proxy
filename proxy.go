package proxy

import (
	"bytes"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Data consisting of request/response proxied through the service
type Data struct {
	StatusCode int
	Request    io.Reader
	Response   io.Reader
}

// ResultCallback function accept execution results for every proxied request (e.g. to collect metrics)
type ResultCallback func(int, error)

// upstream definition for the server we're proxying data to
type upstream struct {
	target url.URL
}

// maximum of idle upstream connections to keep open
const httpMaxIdleConns = 100

// NewHandler creates http.HandlerFunc that proxies requests
// to the given URL
func NewHandler(targetURL string, timeout time.Duration, ch chan<- Data, cb ResultCallback) (http.HandlerFunc, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}
	transport := newTransport(timeout)

	return func(w http.ResponseWriter, r *http.Request) {
		d, err := process(transport, u, w, r)
		if err != nil {
			log.Printf("%s\t%d\t%s\n", r.URL, http.StatusServiceUnavailable, err.Error())
			cb(-1, err)
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}

		log.Printf("%s\t%d\n", r.URL, d.StatusCode)
		cb(d.StatusCode, nil)

		if d.StatusCode == http.StatusOK {
			ch <- d
		}
	}, nil
}

func newTransport(timeout time.Duration) *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: timeout,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          httpMaxIdleConns,
		IdleConnTimeout:       timeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func process(transport *http.Transport,
	target *url.URL, w http.ResponseWriter, r *http.Request) (Data, error) {
	defer r.Body.Close()

	// parse URL of the incoming request and rewrite it to go to upstream target instead
	newurl := rewrite(r.URL, target)

	requestBuf := &bytes.Buffer{}
	req, err := prepareRequest(r, newurl, requestBuf)
	if err != nil {
		return Data{}, err
	}

	res, err := transport.RoundTrip(req)
	if err != nil {
		return Data{}, err
	}

	responseBuf := &bytes.Buffer{}
	defer res.Body.Close()

	copyHeaders(w.Header(), res.Header)
	w.WriteHeader(res.StatusCode)
	_, err = io.Copy(w, io.TeeReader(res.Body, responseBuf))

	return Data{
		StatusCode: res.StatusCode,
		Request:    requestBuf,
		Response:   responseBuf,
	}, err
}

func copyHeaders(dst http.Header, src http.Header) {
	for k := range src {
		dst.Set(k, src.Get(k))
	}
}

// prepare new http.Request with the provided URL, and headers+body taken from the origin
// request
func prepareRequest(r *http.Request, newurl string, buf *bytes.Buffer) (*http.Request, error) {
	req, err := http.NewRequest(r.Method, newurl, io.TeeReader(r.Body, buf))
	if err != nil {
		return nil, err
	}

	copyHeaders(req.Header, r.Header)
	return req, nil
}

// parse URL of the incoming request and rewrite it to go to upstream target instead
func rewrite(source *url.URL, target *url.URL) string {
	u := url.URL{
		Scheme:   target.Scheme,
		Host:     target.Host,
		Path:     source.Path,
		RawQuery: source.RawQuery,
	}

	return u.String()
}
