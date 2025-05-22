// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package auth

import (
	"fmt"
	"net/http"
	"testing"
)

func Test_ErrorValidations(t *testing.T) {
	r := &http.Request{
		Header: http.Header{},
	}
	info := &AuthInfo{
		Realm:     "root",
		UserName:  "admin",
		Email:     "admin@example.com",
		FullName:  "Test Admin",
		SessionID: "abc",
	}
	SetAuthInfoHeader(r, info)
	fmt.Printf("Got - Encoded Auth Info: %s\n", r.Header[httpClientAuthContext][0])
	if r.Header[httpClientAuthContext][0] != "eyJyZWFsbSI6InJvb3QiLCJwcmVmZXJyZWRfdXNlcm5hbWUiOiJhZG1pbiIsImVtYWlsIjoiYWRtaW5AZXhhbXBsZS5jb20iLCJuYW1lIjoiVGVzdCBBZG1pbiIsInNpZCI6ImFiYyJ9" {
		t.Errorf("failed to set the auth info in the header, found invalid value in header")
	}
	found, err := GetAuthInfoHeader(r)
	if err != nil {
		t.Errorf("got error while getting auth info: %s", err)
	}
	if found.Realm != info.Realm {
		t.Errorf("expected realm to be %s, but got %s", info.Realm, found.Realm)
	}
	if found.UserName != info.UserName {
		t.Errorf("expected UserName to be %s, but got %s", info.UserName, found.UserName)
	}
}
