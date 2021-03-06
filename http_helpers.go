package itchio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
)

var dumpApiCalls = os.Getenv("GO_ITCHIO_DEBUG") == "1"

// Get performs an HTTP GET request to the API
func (c *Client) Get(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

func (c *Client) GetResponse(url string, dst interface{}) error {
	resp, err := c.Get(url)
	if err != nil {
		return errors.WithStack(err)
	}

	err = ParseAPIResponse(dst, resp)
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// PostForm performs an HTTP POST request to the API, with url-encoded parameters
func (c *Client) PostForm(url string, data url.Values) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.Do(req)
}

func (c *Client) PostFormResponse(url string, data url.Values, dst interface{}) error {
	resp, err := c.PostForm(url, data)
	if err != nil {
		return errors.WithStack(err)
	}

	err = ParseAPIResponse(dst, resp)
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// Do performs a request (any method). It takes care of JWT or API key
// authentication, sets the propre user agent, has built-in retry,
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	req.Header.Add("Authorization", c.Key)
	req.Header.Set("User-Agent", c.UserAgent)

	var res *http.Response
	var err error

	if dumpApiCalls {
		fmt.Fprintf(os.Stderr, "[request] %s %s\n", req.Method, req.URL)
	}

	retryPatterns := append(c.RetryPatterns, time.Millisecond)

	for _, sleepTime := range retryPatterns {
		res, err = c.HTTPClient.Do(req)
		if err != nil {
			if strings.Contains(err.Error(), "TLS handshake timeout") {
				time.Sleep(sleepTime + time.Duration(rand.Int()%1000)*time.Millisecond)
				continue
			}
			return nil, err
		}

		if res.StatusCode == 503 {
			// Rate limited, try again according to patterns.
			// following https://cloud.google.com/storage/docs/json_api/v1/how-tos/upload#exp-backoff to the letter
			res.Body.Close()
			time.Sleep(sleepTime + time.Duration(rand.Int()%1000)*time.Millisecond)
			continue
		}

		break
	}

	return res, err
}

// MakePath crafts an API url from our configured base URL
func (c *Client) MakePath(format string, a ...interface{}) string {
	return c.MakeValuesPath(nil, format, a...)
}

// MakePath crafts an API url from our configured base URL
func (c *Client) MakeValuesPath(values url.Values, format string, a ...interface{}) string {
	base := strings.Trim(c.BaseURL, "/")
	subPath := strings.Trim(fmt.Sprintf(format, a...), "/")
	path := fmt.Sprintf("%s/%s", base, subPath)
	if len(values) == 0 {
		return path
	}
	return fmt.Sprintf("%s?%s", path, values.Encode())
}

// ParseAPIResponse unmarshals an HTTP response into one of out response
// data structures
func ParseAPIResponse(dst interface{}, res *http.Response) error {
	if res == nil || res.Body == nil {
		return fmt.Errorf("No response from server")
	}

	bodyReader := res.Body
	defer bodyReader.Close()

	if res.StatusCode/100 != 2 {
		return fmt.Errorf("Server returned %s for %s", res.Status, res.Request.URL.String())
	}

	body, err := ioutil.ReadAll(bodyReader)
	if err != nil {
		return errors.WithStack(err)
	}

	if dumpApiCalls {
		fmt.Fprintf(os.Stderr, "[response] %s\n", string(body))
	}

	intermediate := make(map[string]interface{})

	err = json.NewDecoder(bytes.NewReader(body)).Decode(&intermediate)
	if err != nil {
		msg := fmt.Sprintf("JSON decode error: %s\n\nBody: %s\n\n", err.Error(), string(body))
		return errors.New(msg)
	}

	if errorsField, ok := intermediate["errors"]; ok {
		if errorsList, ok := errorsField.([]interface{}); ok {
			var messages []string
			for _, el := range errorsList {
				if errorMessage, ok := el.(string); ok {
					messages = append(messages, errorMessage)
				}
			}
			if len(messages) > 0 {
				return &APIError{Messages: messages}
			}
		}
	}

	intermediate = camelifyMap(intermediate)

	if dumpApiCalls {
		enc := json.NewEncoder(os.Stderr)
		enc.SetIndent("[intermediate] ", "  ")
		enc.Encode(intermediate)
	}

	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName: "json",
		Result:  dst,
		// see https://github.com/itchio/itch/issues/1549
		WeaklyTypedInput: true,
		DecodeHook:       mapstructure.StringToTimeHookFunc(time.RFC3339Nano),
	})
	if err != nil {
		return errors.WithStack(err)
	}

	err = decoder.Decode(intermediate)
	if err != nil {
		msg := fmt.Sprintf("mapstructure decode error: %s\n\nBody: %#v\n\n", err.Error(), intermediate)
		return errors.New(msg)
	}

	return nil
}

// FindBuildFile looks for an uploaded file of the right type
// in a list of file. Returns nil if it can't find one.
func FindBuildFile(fileType BuildFileType, files []*BuildFile) *BuildFile {
	for _, f := range files {
		if f.Type == fileType && f.State == BuildFileStateUploaded {
			return f
		}
	}

	return nil
}
