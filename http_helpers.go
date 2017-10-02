package itchio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-errors/errors"
	"github.com/mitchellh/mapstructure"
)

// Get performs an HTTP GET request to the API
func (c *Client) Get(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
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

// Do performs a request (any method). It takes care of JWT or API key
// authentication, sets the propre user agent, has built-in retry,
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(c.Key, "jwt:") {
		req.Header.Add("Authorization", strings.Split(c.Key, ":")[1])
	}
	req.Header.Set("User-Agent", c.UserAgent)

	var res *http.Response
	var err error

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
	base := strings.Trim(c.BaseURL, "/")
	subPath := strings.Trim(fmt.Sprintf(format, a...), "/")

	var key string
	if strings.HasPrefix(c.Key, "jwt:") {
		key = "jwt"
	} else {
		key = c.Key
	}
	return fmt.Sprintf("%s/%s/%s", base, key, subPath)
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
		return errors.Wrap(err, 0)
	}

	intermediate := make(map[string]interface{})

	err = json.NewDecoder(bytes.NewReader(body)).Decode(&intermediate)
	if err != nil {
		msg := fmt.Sprintf("JSON decode error: %s\n\nBody: %s\n\n", err.Error(), string(body))
		return errors.New(msg)
	}

	if errorsField, ok := intermediate["errors"]; ok {
		if errorsList, ok := errorsField.([]string); ok {
			if len(errorsList) > 0 {
				// TODO: handle multiple errors
				return fmt.Errorf("itch.io API error: %s", strings.Join(errorsList, ","))
			}
		}
	}

	intermediate = camelifyMap(intermediate)

	log.Printf("Decoding intermediate %#v\n", intermediate)

	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName: "json",
		Result:  dst,
	})
	if err != nil {
		return errors.Wrap(err, 0)
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
func FindBuildFile(fileType BuildFileType, files []*BuildFileInfo) *BuildFileInfo {
	for _, f := range files {
		if f.Type == fileType && f.State == BuildFileStateUploaded {
			return f
		}
	}

	return nil
}

// ItchfsURL returns the itchfs:/// url usable to download a given file
// from a given build
func (build BuildInfo) ItchfsURL(file *BuildFileInfo, apiKey string) string {
	return ItchfsURL(build.ID, file.ID, apiKey)
}

// ItchfsURL returns the itchfs:/// url usable to download a given file
// from a given build
func ItchfsURL(buildID int64, fileID int64, apiKey string) string {
	values := url.Values{}
	values.Set("api_key", apiKey)
	return fmt.Sprintf("itchfs:///wharf/builds/%d/files/%d/download?%s",
		buildID, fileID, values.Encode())
}
