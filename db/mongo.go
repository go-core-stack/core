// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Initial reference and motivation taken from
// https://gitlab.com/project-emco/core/emco-base/-/blob/main/src/orchestrator/pkg/infra/db

package db

import (
	"context"
	"net"
	"strconv"

	"github.com/Prabhjot-Sethi/core/errors"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.opentelemetry.io/contrib/instrumentation/go.mongodb.org/mongo-driver/mongo/otelmongo"
)

type mongoStore struct {
	Store
	db *mongo.Database
}

type mongoClient struct {
	StoreClient
	client *mongo.Client
}

type MongoConfig struct {
	Host     string
	Port     string
	Username string
	Password string
}

func (c *MongoConfig) validate() error {
	if c.Host == "" {
		c.Host = "localhost"
	}
	if c.Port == "" || c.Port == "0" {
		c.Port = "27017"
	} else {
		if _, err := strconv.Atoi(c.Port); err != nil {
			return errors.Wrap(errors.InvalidArgument, "invalid database port")
		}
	}
	return nil
}

func NewMongoClient(conf *MongoConfig) (StoreClient, error) {
	if err := conf.validate(); err != nil {
		return nil, err
	}
	uri := "mongodb://" + net.JoinHostPort(conf.Host, conf.Port)
	clientOptions := options.Client()
	// TODO(prabhjot) need to check if this monitor is relevant for us,
	// keeping it asis for now, to be evaluated at later stage
	clientOptions.Monitor = otelmongo.NewMonitor()
	clientOptions.ApplyURI(uri)
	clientOptions.SetAuth(options.Credential{
		AuthMechanism: "SCRAM-SHA-256",
		AuthSource:    "admin", //getSourceIdentifier(),
		Username:      conf.Username,
		Password:      conf.Password,
	})

	client, err := mongo.Connect(context.Background(), clientOptions)
	if err != nil {
		return nil, err
	}

	// make the MongoStore struct hear and then call schema stuff here
	mClient := &mongoClient{
		client: client,
	}
	return mClient, nil
}

// Gets Mongodb Data Store for given database name
// typically while working with mongodb it requires to work on a collection
// which is scoped inside a database construct of mongodb
func (c *mongoClient) GetDataStore(dbName string) Store {
	store := c.client.Database(dbName)

	// make the MongoStore struct hear and then call schema stuff here
	mongoStore := &mongoStore{
		db: store,
	}

	// TODO(prabhjot) we will look forward to enabling references as part of a separate effort
	//go mongoStore.ReadRefSchema(ctx)

	return mongoStore
}

func (c *mongoClient) HealthCheck(ctx context.Context) error {
	return c.client.Ping(ctx, nil)
}
