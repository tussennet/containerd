/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/containerd/containerd/log"
	"github.com/pkg/errors"
	"golang.org/x/net/context/ctxhttp"
)

var (
	// ErrNoToken is returned if a request is successful but the body does not
	// contain an authorization token.
	ErrNoToken = errors.New("authorization server did not include a token in the response")
)

// ErrUnexpectedStatus is returned if a token request returned with unexpected HTTP status
type ErrUnexpectedStatus struct {
	Status     string
	StatusCode int
	Body       []byte
}

func (e ErrUnexpectedStatus) Error() string {
	return fmt.Sprintf("unexpected status: %s", e.Status)
}

func newUnexpectedStatusErr(resp *http.Response) error {
	var b []byte
	if resp.Body != nil {
		b, _ = ioutil.ReadAll(io.LimitReader(resp.Body, 64000)) // 64KB
	}
	return ErrUnexpectedStatus{Status: resp.Status, StatusCode: resp.StatusCode, Body: b}
}

func GenerateTokenOptions(ctx context.Context, host, username, secret string, c Challenge) (TokenOptions, error) {
	realm, ok := c.Parameters["realm"]
	if !ok {
		return TokenOptions{}, errors.New("no realm specified for token auth challenge")
	}

	realmURL, err := url.Parse(realm)
	if err != nil {
		return TokenOptions{}, errors.Wrap(err, "invalid token auth challenge realm")
	}

	to := TokenOptions{
		Realm:    realmURL.String(),
		Service:  c.Parameters["service"],
		Username: username,
		Secret:   secret,
	}

	scope, ok := c.Parameters["scope"]
	if ok {
		to.Scopes = append(to.Scopes, scope)
	} else {
		log.G(ctx).WithField("host", host).Debug("no scope specified for token auth challenge")
	}

	return to, nil
}

// TokenOptions are optios for requesting a token
type TokenOptions struct {
	Realm    string
	Service  string
	Scopes   []string
	Username string
	Secret   string
}

type postTokenResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"`
	IssuedAt     time.Time `json:"issued_at"`
	Scope        string    `json:"scope"`
}

func FetchTokenWithOAuth(ctx context.Context, client *http.Client, headers http.Header, clientID string, to TokenOptions) (string, error) {
	form := url.Values{}
	if len(to.Scopes) > 0 {
		form.Set("scope", strings.Join(to.Scopes, " "))
	}
	form.Set("service", to.Service)
	form.Set("client_id", clientID)

	if to.Username == "" {
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", to.Secret)
	} else {
		form.Set("grant_type", "password")
		form.Set("username", to.Username)
		form.Set("password", to.Secret)
	}

	req, err := http.NewRequest("POST", to.Realm, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	if headers != nil {
		for k, v := range headers {
			req.Header[k] = append(req.Header[k], v...)
		}
	}

	resp, err := ctxhttp.Do(ctx, client, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", errors.WithStack(newUnexpectedStatusErr(resp))
	}

	decoder := json.NewDecoder(resp.Body)

	var tr postTokenResponse
	if err = decoder.Decode(&tr); err != nil {
		return "", errors.Errorf("unable to decode token response: %s", err)
	}

	return tr.AccessToken, nil
}

type getTokenResponse struct {
	Token        string    `json:"token"`
	AccessToken  string    `json:"access_token"`
	ExpiresIn    int       `json:"expires_in"`
	IssuedAt     time.Time `json:"issued_at"`
	RefreshToken string    `json:"refresh_token"`
}

// FetchToken fetches a token using a GET request
func FetchToken(ctx context.Context, client *http.Client, headers http.Header, to TokenOptions) (string, error) {
	req, err := http.NewRequest("GET", to.Realm, nil)
	if err != nil {
		return "", err
	}

	if headers != nil {
		for k, v := range headers {
			req.Header[k] = append(req.Header[k], v...)
		}
	}

	reqParams := req.URL.Query()

	if to.Service != "" {
		reqParams.Add("service", to.Service)
	}

	for _, scope := range to.Scopes {
		reqParams.Add("scope", scope)
	}

	if to.Secret != "" {
		req.SetBasicAuth(to.Username, to.Secret)
	}

	req.URL.RawQuery = reqParams.Encode()

	resp, err := ctxhttp.Do(ctx, client, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", errors.WithStack(newUnexpectedStatusErr(resp))
	}

	decoder := json.NewDecoder(resp.Body)

	var tr getTokenResponse
	if err = decoder.Decode(&tr); err != nil {
		return "", errors.Errorf("unable to decode token response: %s", err)
	}

	// `access_token` is equivalent to `token` and if both are specified
	// the choice is undefined.  Canonicalize `access_token` by sticking
	// things in `token`.
	if tr.AccessToken != "" {
		tr.Token = tr.AccessToken
	}

	if tr.Token == "" {
		return "", ErrNoToken
	}

	return tr.Token, nil
}
