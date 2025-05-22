// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package auth

const (
	// Internal Auth Context Header, carries information of the
	// client that has been authenticated.
	// Where content itself will be of usual string format, which
	// is obtained by json marshaling of struct AuthInfo followed
	// by base64 encoding of the json marshaled content.
	//
	// This is usually Added by Auth Gateway, if present it
	// indicates that authentication is successfully performed
	// by Auth Gateway.
	HttpClientAuthContext = "Auth-Info"

	// grpc gateway will typically move the header to lowercase
	GrpcClientAuthContext = "auth-info"
)
