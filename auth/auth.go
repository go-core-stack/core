// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

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

// struct identifier for the context
type authInfo struct{}

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
	r.Header.Set(HttpClientAuthContext, val)
	return nil
}

// gets Auth Info Header available in the Http Request
func GetAuthInfoHeader(r *http.Request) (*AuthInfo, error) {
	val := r.Header.Get(HttpClientAuthContext)
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

// extract the header information from the GRPC context
func extractHeader(ctx context.Context, header string) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.NotFound, "No Metadata available in incoming message")
	}

	hValue, ok := md[header]
	if !ok {
		return "", status.Errorf(codes.NotFound, "missing header: %s", header)
	}

	if len(hValue) != 1 {
		return "", status.Errorf(codes.NotFound, "no value associated with header: %s", header)
	}

	return hValue[0], nil
}

// Processes the headers available in context, to validate that the authentication is already performed
func ProcessAuthInfo(ctx context.Context) (context.Context, error) {
	val, err := extractHeader(ctx, GrpcClientAuthContext)
	if err != nil {
		return ctx, errors.Wrapf(errors.Unauthorized, "failed to extract auth info header: %s", err)
	}

	b, err := base64.RawURLEncoding.DecodeString(val)
	if err != nil {
		return ctx, errors.Wrapf(errors.Unauthorized, "invalid user info received: %s", err)
	}

	info := &AuthInfo{}
	err = json.Unmarshal(b, info)
	if err != nil {
		return ctx, errors.Wrapf(errors.Unauthorized, "failed to get user info from header: %s", err)
	}

	// create new context with value of the auth info
	authCtx := context.WithValue(ctx, authInfo{}, info)
	return authCtx, nil
}

// gets Auth Info from Context available in the Http Request
func GetAuthInfoFromContext(ctx context.Context) (*AuthInfo, error) {
	val := ctx.Value(authInfo{})
	switch info := val.(type) {
	case *AuthInfo:
		return info, nil
	default:
		return nil, errors.Wrapf(errors.NotFound, "auth info not found")
	}
}

// delete the Auth info header from the given HTTP request
func DeleteAuthInfoHeader(r *http.Request) {
	r.Header.Del(HttpClientAuthContext)
}
