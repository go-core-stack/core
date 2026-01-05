// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package values

import "os"

const (
	// Environment variable name providing mongo configdb username
	MongoConfigDBUserNameEnv = "MONGO_CONFIGDB_USERNAME"

	// Default value for the mongo configdb username
	DefaultMongoConfigDBUserName = "root"

	// Environment variable name providing mongo configdb password
	MongoConfigDBPasswordEnv = "MONGO_CONFIGDB_PASSWORD"

	// Default value for the mongo configdb password
	DefaultMongoConfigDBPassword = "password"
)

// Get configured mongodb credentials
func GetMongoConfigDBCredentials() (string, string) {
	user, ok := os.LookupEnv(MongoConfigDBUserNameEnv)
	if !ok {
		// if user env is not set return default values even for password
		return DefaultMongoConfigDBUserName, DefaultMongoConfigDBPassword
	}
	pass, ok := os.LookupEnv(MongoConfigDBPasswordEnv)
	if !ok {
		// if password env is not set return default values even for user
		return DefaultMongoConfigDBUserName, DefaultMongoConfigDBPassword
	}
	return user, pass
}
