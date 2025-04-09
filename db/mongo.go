// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Initial reference and motivation taken from
// https://gitlab.com/project-emco/core/emco-base/-/blob/main/src/orchestrator/pkg/infra/db

package db

import (
	"context"
	"net"
	"strconv"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.opentelemetry.io/contrib/instrumentation/go.mongodb.org/mongo-driver/mongo/otelmongo"

	"github.com/Prabhjot-Sethi/core/errors"
)

type mongoCollection struct {
	StoreCollection
	parent  *mongoStore // handler for the parent mongo DB object
	colName string      // name of the collection this collection object is working with
	col     *mongo.Collection
}

// inserts one entry with given key and data to the collection
// returns errors if entry already exists or if there is a connection
// error with the database server
func (c *mongoCollection) InsertOne(ctx context.Context, key interface{}, data interface{}) error {
	if data == nil {
		return errors.Wrap(errors.InvalidArgument, "db Insert error: No data to store")
	}
	if key == nil {
		return errors.Wrap(errors.InvalidArgument, "db Insert error: No Key specified to store")
	}

	// convert data to bson document for transacting with mongo db library
	marshaledData, err := bson.Marshal(data)
	if err != nil {
		// return if any error occured
		return err
	}

	bd := bson.D{}
	err = bson.Unmarshal(marshaledData, &bd)
	if err != nil {
		// return if any error occured
		return err
	}

	// key is already nil checked
	// set the primary key to specified key.
	//
	// TODO(prabhjot) check if we want to allow nil key at some point
	// Typically mongodb allows inserts with key not specified
	// where it auto allocates the key to random id
	primKey := bson.E{
		Key:   "_id", // set primary key
		Value: key,
	}
	bd = append(bd, primKey)

	_, err = c.col.InsertOne(ctx, bd)
	if err != nil {
		// TODO(prabhjot) we may need to identify and differentiate
		// Already Exist error here.
		return err
	}
	return nil
}

// remove one entry from the collection matching the given key
func (c *mongoCollection) DeleteOne(ctx context.Context, key interface{}) error {
	resp, err := c.col.DeleteOne(ctx, bson.M{"_id": key})
	if err != nil {
		// TODO(prabhjot) we may need to identify and differentiate
		// Not found error here
		return err
	}
	if resp.DeletedCount == 0 {
		return errors.Wrap(errors.NotFound, "No Document found")
	}

	return nil
}

type mongoStore struct {
	Store
	db *mongo.Database
}

func (s *mongoStore) GetCollection(name string) StoreCollection {
	handle := s.db.Collection(name)
	c := &mongoCollection{
		parent:  s,
		colName: name,
		col:     handle,
	}

	return c
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

// gets Mongo DB collection for given collection name
// inside a database specified with db name
func (c *mongoClient) GetCollection(dbName, col string) StoreCollection {
	s := c.GetDataStore(dbName)
	return s.GetCollection(col)
}

func (c *mongoClient) HealthCheck(ctx context.Context) error {
	return c.client.Ping(ctx, nil)
}
