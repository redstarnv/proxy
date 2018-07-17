package proxy

import (
	"bytes"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"time"
)

// Data consisting of request/response proxied through the service
type Data struct {
	StatusCode int
	Request    io.Reader
	Response   io.Reader
	Error      error
	Times      Times
	Source     string
}

// upstream definition for the server we're proxying data to
type upstream struct {
	target url.URL
}

// Times is struct to store request time
type Times struct {
	Start                time.Time
	WroteRequest         time.Time
	GotFirstResponseByte time.Time
	End                  time.Time
}

// maximum of idle upstream connections to keep open
const httpMaxIdleConns = 100

// NewHandler creates http.HandlerFunc that proxies requests
// to the given URL
func NewHandler(targetURL string, timeout time.Duration, ch chan<- Data) (http.HandlerFunc, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}
	transport := newTransport(timeout)

	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var d Data
		d.Times.Start = time.Now()
		d.Error = handleRequest(transport, w, &d, r, u)
		d.Times.End = time.Now()

		ch <- d

		if d.Error != nil {
			log.Printf("%s\t%d\t%s\n", r.URL, http.StatusServiceUnavailable, d.Error.Error())
			http.Error(w, d.Error.Error(), http.StatusServiceUnavailable)
			return
		}

		log.Printf("%s\t%d\n", r.URL, d.StatusCode)

	}, nil
}

func handleRequest(transport *http.Transport, w http.ResponseWriter, d *Data, r *http.Request, u *url.URL) error {
	req, err := prepareRequest(r, d, u)
	if err != nil {
		return err
	}

	d.Source = r.Header.Get("Source")

	return process(transport, d, req, w)
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

func process(transport *http.Transport, d *Data, req *http.Request, w http.ResponseWriter) error {
	res, err := transport.RoundTrip(req)
	if err != nil {
		d.StatusCode = http.StatusServiceUnavailable
		return err
	}
	d.StatusCode = res.StatusCode

	responseBuf := &bytes.Buffer{}
	defer res.Body.Close()

	copyHeaders(w.Header(), res.Header)
	w.WriteHeader(res.StatusCode)
	_, err = io.Copy(w, io.TeeReader(res.Body, responseBuf))

	d.Response = responseBuf
	return err
}

func copyHeaders(dst http.Header, src http.Header) {
	for k := range src {
		dst.Set(k, src.Get(k))
	}
}

// prepare new http.Request with the provided URL, and headers+body taken from the origin
// request
func prepareRequest(r *http.Request, d *Data, target *url.URL) (*http.Request, error) {
	// parse URL of the incoming request and rewrite it to go to upstream target instead
	newurl := rewrite(r.URL, target)
	buf := &bytes.Buffer{}

	req, err := http.NewRequest(r.Method, newurl, io.TeeReader(r.Body, buf))
	d.Request = buf
	if err != nil {
		return nil, err
	}

	copyHeaders(req.Header, r.Header)

	trace := &httptrace.ClientTrace{
		WroteRequest: func(_ httptrace.WroteRequestInfo) {
			d.Times.WroteRequest = time.Now()
		},
		GotFirstResponseByte: func() {
			d.Times.GotFirstResponseByte = time.Now()
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

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
