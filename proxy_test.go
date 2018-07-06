package proxy_test

import (
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	proxy "github.com/redstarnv/proxy"
	"github.com/stretchr/testify/require"
)

// validate http body
func validateBody(t *testing.T, body io.ReadCloser, expectedBody string) {
	reqbody, err := ioutil.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, expectedBody, string(reqbody))
}

// validate http headers
func validateHeaders(t *testing.T, headers http.Header, expectedHeaders map[string]string) {
	for k, v := range expectedHeaders {
		require.Equal(t, v, headers.Get(k))
	}
}

func writeResponse(w http.ResponseWriter, body string, headers map[string]string) {
	for k, v := range headers {
		w.Header().Set(k, v)
	}

	io.WriteString(w, body)
}

const timeout = 1 * time.Second

var requestBody = "<xml>some request</xml>"
var requestHeaders = map[string]string{
	"Content-Type":     "text/xml",
	"X-Request-Header": "request header value",
}

var responseBody = "bam"
var responseHeaders = map[string]string{
	"Content-Type":      "application/xml",
	"X-Response-Header": "response header value",
}

func sendRequest(t *testing.T, target *httptest.Server, mchan chan proxy.Data) *http.Response {
	cb := func(status int, err error) {}

	h, err := proxy.NewHandler(target.URL, timeout, mchan, cb)
	require.NoError(t, err)

	prx := httptest.NewServer(h)

	req, err := http.NewRequest(
		http.MethodPost,
		prx.URL+"/some/path",
		strings.NewReader(requestBody),
	)
	require.NoError(t, err)

	req.Header.Set("Content-Type", "text/xml")
	req.Header.Set("X-Request-Header", "request header value")

	res, err := prx.Client().Do(req)
	require.NoError(t, err)
	prx.Close()

	return res
}

func TestSuccessfulRequestProxying(t *testing.T) {
	mchan := make(chan proxy.Data, 10)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validateBody(t, r.Body, requestBody)
		validateHeaders(t, r.Header, requestHeaders)
		writeResponse(w, responseBody, responseHeaders)
	}))
	defer target.Close()

	res := sendRequest(t, target, mchan)

	require.Equal(t, http.StatusOK, res.StatusCode)
	validateBody(t, res.Body, responseBody)
	validateHeaders(t, res.Header, responseHeaders)

	// verify that data message has been published
	select {
	case data := <-mchan:
		repRequest, err := ioutil.ReadAll(data.Request)
		require.NoError(t, err)
		require.Equal(t, requestBody, string(repRequest), "reported request must match")

		repResponse, err := ioutil.ReadAll(data.Response)
		require.NoError(t, err)
		require.Equal(t, responseBody, string(repResponse), "reported response must match")
	default:
		require.Fail(t, "Proxy must have published a data item")
	}
}

func TestErroredRequestProxying(t *testing.T) {
	mchan := make(chan proxy.Data, 10)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("500 - boom"))
	}))
	defer target.Close()

	res := sendRequest(t, target, mchan)

	require.Equal(t, http.StatusInternalServerError, res.StatusCode)
	validateBody(t, res.Body, "500 - boom")

	// verify that data message has not been published
	select {
	case _ = <-mchan:
		require.Fail(t, "Proxy must not have published a data item")
	default:
	}
}

func TestBrokenUpstreamConnection(t *testing.T) {
	mchan := make(chan proxy.Data, 10)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("500 - boom"))
	}))
	target.Close()

	res := sendRequest(t, target, mchan)

	require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)

	// verify that data message has not been published
	select {
	case _ = <-mchan:
		require.Fail(t, "Proxy must not have published a data item")
	default:
	}
}

func TestUpstreamTimeout(t *testing.T) {
	mchan := make(chan proxy.Data, 10)
	stop := make(chan bool, 1)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stop:
			// do nothing - just return from handlerFunc, request has been
			// processed already
		case <-time.After(2 * timeout):
			require.Fail(t, "request didn't die on timeout")
		}
	}))

	res := sendRequest(t, target, mchan)
	stop <- true
	require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)

	// verify that data message has not been published
	target.Close()
	select {
	case _ = <-mchan:
		require.Fail(t, "Proxy must not have published a data item")
	default:
	}
}
