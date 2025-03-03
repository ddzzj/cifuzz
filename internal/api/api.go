package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/net/proxy"
	"golang.org/x/sync/errgroup"
	"golang.org/x/term"

	"code-intelligence.com/cifuzz/internal/cmd/remoterun/progress"
	"code-intelligence.com/cifuzz/internal/cmdutils"
	"code-intelligence.com/cifuzz/pkg/log"
	"code-intelligence.com/cifuzz/util/stringutil"
)

// APIError is returned when a REST request returns a status code other
// than 200 OK
type APIError struct {
	err        error
	StatusCode int
}

func (e APIError) Error() string {
	return e.err.Error()
}

func (e APIError) Format(s fmt.State, verb rune) {
	if formatter, ok := e.err.(fmt.Formatter); ok {
		formatter.Format(s, verb)
	} else {
		_, _ = io.WriteString(s, e.Error())
	}
}

func (e APIError) Unwrap() error {
	return e.err
}

func responseToAPIError(resp *http.Response) error {
	msg := resp.Status
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &APIError{StatusCode: resp.StatusCode, err: errors.New(msg)}
	}
	apiResp := struct {
		Code    int
		Message string
	}{}
	err = json.Unmarshal(body, &apiResp)
	if err != nil {
		return &APIError{StatusCode: resp.StatusCode, err: errors.Errorf("%s: %s", msg, string(body))}
	}
	return &APIError{StatusCode: resp.StatusCode, err: errors.Errorf("%s: %s", msg, apiResp.Message)}
}

// ConnectionError is returned when a REST request fails to connect to the API
type ConnectionError struct {
	err error
}

func (e ConnectionError) Error() string {
	return e.err.Error()
}

func (e ConnectionError) Unwrap() error {
	return e.err
}

// WrapConnectionError wraps an error returned by the API client in a
// ConnectionError to avoid having the error message printed when the error is
// handled.
func WrapConnectionError(err error) error {
	return &ConnectionError{err}
}

type APIClient struct {
	Server    string
	UserAgent string
}

var FeaturedProjectsOrganization = "organizations/1"

type Artifact struct {
	DisplayName  string `json:"display-name"`
	ResourceName string `json:"resource-name"`
}

func NewClient(server string, version string) *APIClient {
	return &APIClient{
		Server:    server,
		UserAgent: "cifuzz/" + version + " " + runtime.GOOS + "-" + runtime.GOARCH,
	}
}

func (client *APIClient) UploadBundle(path string, projectName string, token string) (*Artifact, error) {
	signalHandlerCtx, cancelSignalHandler := context.WithCancel(context.Background())
	routines, routinesCtx := errgroup.WithContext(context.Background())

	// Cancel the routines context when receiving a termination signal
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	routines.Go(func() error {
		select {
		case <-signalHandlerCtx.Done():
			return nil
		case s := <-sigs:
			log.Warnf("Received %s", s.String())
			return cmdutils.NewSignalError(s.(syscall.Signal))
		}
	})

	// Use a pipe to avoid reading the artifacts into memory at once
	r, w := io.Pipe()
	m := multipart.NewWriter(w)

	// Write the artifacts to the pipe
	routines.Go(func() error {
		defer w.Close()
		defer m.Close()

		part, err := m.CreateFormFile("fuzzing-artifacts", path)
		if err != nil {
			return errors.WithStack(err)
		}

		fileInfo, err := os.Stat(path)
		if err != nil {
			return errors.WithStack(err)
		}

		f, err := os.Open(path)
		if err != nil {
			return errors.WithStack(err)
		}
		defer f.Close()

		var reader io.Reader
		printProgress := term.IsTerminal(int(os.Stdout.Fd()))
		if printProgress {
			fmt.Println("Uploading...")
			reader = progress.NewReader(f, fileInfo.Size(), "Upload complete")
		} else {
			reader = f
		}

		_, err = io.Copy(part, reader)
		return errors.WithStack(err)
	})

	// Send a POST request with what we read from the pipe. The request
	// gets cancelled with the routines context is cancelled, which
	// happens if an error occurs in the io.Copy above or the user if
	// cancels the operation.
	var body []byte
	routines.Go(func() error {
		defer r.Close()
		defer cancelSignalHandler()
		url, err := url.JoinPath(client.Server, "v2", projectName, "artifacts", "import")
		if err != nil {
			return errors.WithStack(err)
		}
		req, err := http.NewRequestWithContext(routinesCtx, "POST", url, r)
		if err != nil {
			return errors.WithStack(err)
		}

		req.Header.Set("User-Agent", client.UserAgent)
		req.Header.Set("Content-Type", m.FormDataContentType())
		req.Header.Add("Authorization", "Bearer "+token)

		httpClient := &http.Client{Transport: getCustomTransport()}
		resp, err := httpClient.Do(req)
		if err != nil {
			return errors.WithStack(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return responseToAPIError(resp)
		}

		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return errors.WithStack(err)
		}

		return nil
	})

	err := routines.Wait()
	if err != nil {
		return nil, err
	}

	artifact := &Artifact{}
	err = json.Unmarshal(body, artifact)
	if err != nil {
		err = errors.WithStack(err)
		log.Errorf(err, "Failed to parse response from upload bundle API call: %s", err.Error())
		return nil, cmdutils.WrapSilentError(err)
	}

	return artifact, nil
}

func (client *APIClient) StartRemoteFuzzingRun(artifact *Artifact, token string) (string, error) {
	url, err := url.JoinPath("/v1", artifact.ResourceName+":run")
	if err != nil {
		return "", err
	}
	resp, err := client.sendRequest("POST", url, nil, token)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", responseToAPIError(resp)
	}

	// Get the campaign run name from the response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.WithStack(err)
	}
	var objmap map[string]json.RawMessage
	err = json.Unmarshal(body, &objmap)
	if err != nil {
		return "", errors.WithStack(err)
	}
	campaignRunNameJSON, ok := objmap["name"]
	if !ok {
		err = errors.Errorf("Server response doesn't include run name: %v", stringutil.PrettyString(objmap))
		log.Error(err)
		return "", cmdutils.WrapSilentError(err)
	}
	var campaignRunName string
	err = json.Unmarshal(campaignRunNameJSON, &campaignRunName)
	if err != nil {
		return "", errors.WithStack(err)
	}

	return campaignRunName, nil
}

// sendRequest sends a request to the API server with a default timeout of 30 seconds.
func (client *APIClient) sendRequest(method string, endpoint string, body io.Reader, token string) (*http.Response, error) {
	// we use 30 seconds as a conservative timeout for the API server to
	// respond to a request. We might have to revisit this value in the future
	// after the rollout of our API features.
	timeout := 30 * time.Second
	return client.sendRequestWithTimeout(method, endpoint, body, token, timeout)
}

// sendRequestWithTimeout sends a request to the API server with a timeout.
func (client *APIClient) sendRequestWithTimeout(method string, endpoint string, body io.Reader, token string, timeout time.Duration) (*http.Response, error) {
	url, err := url.JoinPath(client.Server, endpoint)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, body)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	req.Header.Set("User-Agent", client.UserAgent)
	req.Header.Add("Authorization", "Bearer "+token)

	httpClient := &http.Client{Transport: getCustomTransport(), Timeout: timeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, WrapConnectionError(errors.WithStack(err))
	}

	return resp, nil
}

func (client *APIClient) IsTokenValid(token string) (bool, error) {
	// TOOD: Change this to use another check without querying projects
	_, err := client.ListProjects(token)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			if apiErr.StatusCode == 401 {
				return false, nil
			}
		}
		return false, err
	}
	return true, nil
}

func validateURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return errors.WithStack(err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.Errorf("unsupported protocol scheme %q", u.Scheme)
	}
	return nil
}

func ValidateAndNormalizeServerURL(server string) (string, error) {
	// Check if the server option is a valid URL
	err := validateURL(server)
	if err != nil {
		// See if prefixing https:// makes it a valid URL
		err = validateURL("https://" + server)
		if err != nil {
			log.Error(err, fmt.Sprintf("server %q is not a valid URL", server))
		}
		server = "https://" + server
	}

	// normalize server URL
	url, err := url.JoinPath(server)
	if err != nil {
		return "", err
	}
	return url, nil
}

func getCustomTransport() *http.Transport {
	// it is not possible to use the default Proxy Environment because
	// of https://github.com/golang/go/issues/24135
	dialer := proxy.FromEnvironment()
	dialContext := func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.Dial(network, address)
	}
	return &http.Transport{DialContext: dialContext}
}
