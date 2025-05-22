// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/Prabhjot-Sethi/core/errors"
)

// Auth construct obtained as part of the auth action being performed
// while processing a request, this is json tagged to allow passing
// the inforamtion internally in the system between the microservices
// we can validate entities like user, devices, service accounts etc
type AuthInfo struct {
	Realm     string `json:"realm,omitempty"`
	UserName  string `json:"preferred_username"`
	Email     string `json:"email,omitempty"`
	FullName  string `json:"name,omitempty"`
	SessionID string `json:"sid"`
}

// Sets Auth Info Header in the provided Http Request typically will
// be used only by the entity that has performed that authentication
// on the given http request already and has the relevant Auth Info
// Context.
func SetAuthInfoHeader(r *http.Request, info *AuthInfo) error {
	b, err := json.Marshal(info)
	if err != nil {
		return errors.Wrapf(errors.InvalidArgument, "failed to generate user info: %s", err)
	}
	val := base64.RawURLEncoding.EncodeToString(b)
	r.Header.Set(httpClientAuthContext, val)
	return nil
}

// gets Auth Info Header available in the Http Request
func GetAuthInfoHeader(r *http.Request) (*AuthInfo, error) {
	val := r.Header.Get(httpClientAuthContext)
	if val == "" {
		return nil, errors.Wrapf(errors.NotFound, "Auth info not available in the http request")
	}
	b, err := base64.RawURLEncoding.DecodeString(val)
	if err != nil {
		return nil, errors.Wrapf(errors.InvalidArgument, "invalid user info received: %s", err)
	}
	info := &AuthInfo{}
	err = json.Unmarshal(b, info)
	if err != nil {
		return nil, errors.Wrapf(errors.InvalidArgument, "failed to get user info from header: %s", err)
	}
	return info, nil
}

// delete the Auth info header from the given HTTP request
func DeleteAuthInfoHeader(r *http.Request) {
	r.Header.Del(httpClientAuthContext)
}
