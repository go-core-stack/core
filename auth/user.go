// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package auth

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
