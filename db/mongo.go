// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Initial reference and motivation taken from
// https://gitlab.com/project-emco/core/emco-base/-/blob/main/src/orchestrator/pkg/infra/db

package db

import (
	"context"
	"log"
	"net"
	"reflect"
	"strconv"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"

	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/utils"
)

type mongoCollection struct {
	StoreCollection
	parent  *mongoStore // handler for the parent mongo DB object
	colName string      // name of the collection this collection object is working with
	col     *mongo.Collection
	keyType reflect.Type
}

// interprets mongo db error and returns library parsable error codes
func interpretMongoError(err error) error {
	if mongo.IsDuplicateKeyError(err) {
		return errors.Wrap(errors.AlreadyExists, err.Error())
	}
	if err == mongo.ErrNoDocuments {
		return errors.Wrap(errors.NotFound, err.Error())
	}
	return err
}

// Set KeyType for the collection, this is not mandatory
// while the key type will be used by the interface implementer
// mainly for Watch Callback for providing decoded key, if not
// set watch will be working with the default decoders of
// interface implementer
// only pointer key type is supported as of now
// returns error if the key type is not a pointer
func (c *mongoCollection) SetKeyType(keyType reflect.Type) error {
	if keyType.Kind() != reflect.Ptr {
		// return error, as only pointer key type is supported
		return errors.Wrap(errors.InvalidArgument, "key type is not a pointer")
	}
	c.keyType = keyType
	return nil
}

// inserts one entry with given key and data to the collection
// returns errors if entry already exists or if there is a connection
// error with the database server
func (c *mongoCollection) InsertOne(ctx context.Context, key any, data any) error {
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
		// identify and differentiate Already Exist error here.
		return interpretMongoError(err)
	}
	return nil
}

// inserts or updates one entry with given key and data to the collection
// acts based on the flag passed for upsert
// returns errors if entry not found while upsert flag is false or if
// there is a connection error with the database server
func (c *mongoCollection) UpdateOne(ctx context.Context, key any, data any, upsert bool) error {
	if data == nil {
		return errors.Wrap(errors.InvalidArgument, "db Insert error: No data to store")
	}
	if key == nil {
		return errors.Wrap(errors.InvalidArgument, "db Insert error: No Key specified to store")
	}

	opts := options.UpdateOne().SetUpsert(upsert)
	resp, err := c.col.UpdateOne(
		ctx,
		bson.M{"_id": key},
		bson.D{
			{Key: "$set", Value: data},
		},
		opts)

	if err != nil {
		return interpretMongoError(err)
	}

	// check there should be at least one entry in matched count
	// or upserted count to not return an error here
	if resp.MatchedCount != 0 && resp.UpsertedCount != 0 {
		return errors.Wrap(errors.NotFound, "No Document found")
	}

	return nil
}

// Find one entry from the store collection for the given key, where the data
// value is returned based on the object type passed to it
func (c *mongoCollection) FindOne(ctx context.Context, key any, data any) error {
	resp := c.col.FindOne(ctx, bson.M{"_id": key})
	// decode the value returned by the mongodb client into the data
	// object passed by the caller
	if err := resp.Decode(data); err != nil {
		// TODO(prabhjot) might have to identify not found error
		return interpretMongoError(err)
	}
	return nil
}

// Find multiple entries from the store collection for the given filter, where the data
// value is returned as a list based on the object type passed to it
func (c *mongoCollection) FindMany(ctx context.Context, filter any, data any, opts ...any) error {
	if filter == nil {
		filter = bson.D{}
	}
	var findOpts []options.Lister[options.FindOptions]
	for _, opt := range opts {
		val, ok := opt.(options.Lister[options.FindOptions])
		if !ok {
			return errors.Wrapf(errors.InvalidArgument, "Invalid option type %T passed to FindMany", opt)
		}
		findOpts = append(findOpts, val)
	}
	cursor, err := c.col.Find(ctx, filter, findOpts...)
	if err != nil {
		return interpretMongoError(err)
	}
	if err = cursor.All(ctx, data); err != nil {
		return err
	}
	return nil
}

// Return count of entries matching the provided filter
func (c *mongoCollection) Count(ctx context.Context, filter any) (int64, error) {
	if filter == nil {
		filter = bson.D{}
	}
	count, err := c.col.CountDocuments(ctx, filter)
	if err != nil {
		return 0, interpretMongoError(err)
	}
	return count, nil
}

// remove one entry from the collection matching the given key
func (c *mongoCollection) DeleteOne(ctx context.Context, key any) error {
	resp, err := c.col.DeleteOne(ctx, bson.M{"_id": key})
	if err != nil {
		// TODO(prabhjot) we may need to identify and differentiate
		// Not found error here
		return interpretMongoError(err)
	}
	if resp.DeletedCount == 0 {
		return errors.Wrap(errors.NotFound, "No Document found")
	}

	return nil
}

// Delete Many entries matching the delete criteria
// returns number of entries deleted and if there is any error processing the request
func (c *mongoCollection) DeleteMany(ctx context.Context, filter any) (int64, error) {
	resp, err := c.col.DeleteMany(ctx, filter)
	if err != nil {
		return 0, interpretMongoError(err)
	}
	if resp.DeletedCount == 0 {
		return 0, errors.Wrap(errors.NotFound, "No matching entries found to delete")
	}
	return resp.DeletedCount, nil
}

// watch allows getting notified whenever a change happens to a document
// in the collection
// allow provisiong for a filter to be passed on, where the callback
// function to receive only conditional notifications of the events
// listener is interested about
func (c *mongoCollection) Watch(ctx context.Context, filter any, cb WatchCallbackfn) error {
	if filter == nil {
		// if passed filter is nil, initialize it to empty pipeline object
		filter = mongo.Pipeline{}
	}
	switch v := filter.(type) {
	case mongo.Pipeline:
		// we are ok to proceed further
		break
	default:
		return errors.Wrapf(errors.InvalidArgument, "Invalid watch filter pipeline type specified, %v", v)
	}
	// start watching on the collection with passed context
	stream, err := c.col.Watch(ctx, filter)
	if err != nil {
		return err
	}

	// run the loop on stream in a separate go routine
	// allowing the watch starter to resume control and work with
	// managing Watch stream by virtue of passed context
	go func() {
		// take a snapshot of keyTpe for processing watch
		keyType := c.keyType
		// ensure closing of the open stream in case of returning from here
		// keeping the handles and stack clean
		// Note: this may not be required, if loop doesn't require it
		// but still it is safe to keep ensuring appropriate cleanup
		defer func() {
			// ignore the error returned by stream close as of now
			_ = stream.Close(context.Background())
		}()
		defer func() {
			if !errors.Is(ctx.Err(), context.Canceled) {
				// panic if the return from this function is not
				// due to context being canceled
				log.Panicf("End of stream observed due to error %s", stream.Err())
			}
		}()
		for stream.Next(ctx) {
			var data bson.M
			if err := stream.Decode(&data); err != nil {
				log.Printf("Closing watch due to decoding error %s", err)
				return
			}

			op, ok := data["operationType"].(string)
			if !ok {
				log.Printf("Closing watch due to error, unable to find decode operation type ")
				return
			}

			var dk bson.M
			mdk, err := bson.Marshal(data["documentKey"])
			if err != nil {
				log.Printf("Closing watch due to error, while bson Marshal dock Key : %q", err)
				return
			}

			err = bson.Unmarshal(mdk, &dk)
			if err != nil {
				log.Printf("Closing watch due to error, while bson Unmarshal doc key : %q", err)
				return
			}

			bKey, ok := dk["_id"].(bson.D)
			if !ok {
				log.Printf("Closing watch due to error, unable to find id")
				return
			}

			// key that will be shared with callback function
			var key any
			if keyType != nil {
				key = reflect.New(keyType.Elem()).Interface()
			} else {
				key = bson.M{}
			}

			marshaledData, err := bson.Marshal(bKey)
			if err != nil {
				log.Printf("Closing watch due to error, while bson Marshal : %q", err)
				return
			}

			err = bson.Unmarshal(marshaledData, key)
			if err != nil {
				log.Printf("Closing watch due to error, while bson Unmarshal to key : %q", err)
				return
			}
			cb(op, key)
		}
	}()

	return nil
}

// startEventLogger starts the event logger for the collection and trigger logger for events
func (c *mongoCollection) startEventLogger(ctx context.Context, eventType reflect.Type, timestamp *bson.Timestamp) error {
	// TODO(prabhjot) if we may need to enable pre and post images for change streams
	// to get the full document before and after the change, probably not needed
	// as of now, as the continuity or sequence of the events will anyway provide
	// the complete context of the change to the object
	/*
		func() {
			// Enable pre-images on the collection
			cmd := bson.D{
				{Key: "collMod", Value: c.colName},
				{Key: "changeStreamPreAndPostImages", Value: bson.D{
					{Key: "enabled", Value: true},
				}},
			}

			var result bson.M
			if err := c.col.Database().RunCommand(ctx, cmd).Decode(&result); err != nil {
				log.Fatalf("Failed to enable pre-images: %v", err)
			}
		}()
	*/

	opts := options.ChangeStream()
	opts.SetFullDocumentBeforeChange(options.WhenAvailable)
	opts.SetFullDocument(options.WhenAvailable)

	if timestamp != nil {
		opts.SetStartAtOperationTime(timestamp)
	}

	// start watching on the collection with required context
	stream, err := c.col.Watch(ctx, mongo.Pipeline{}, opts)
	if err != nil {
		return err
	}

	// run the loop on stream in a separate go routine
	// allowing the watch starter to resume control and work with
	// managing Watch stream by virtue of passed context
	go func() {
		// ensure closing of the open stream in case of returning from here
		// keeping the handles and stack clean
		// Note: this may not be required, if loop doesn't require it
		// but still it is safe to keep ensuring appropriate cleanup
		defer func() {
			// ignore the error returned by stream close as of now
			_ = stream.Close(context.Background())
		}()
		defer func() {
			if !errors.Is(ctx.Err(), context.Canceled) {
				// panic if the return from this function is not
				// due to context being canceled
				log.Panicf("End of stream observed due to error %s", stream.Err())
			}
		}()
		for stream.Next(ctx) {
			event := reflect.New(eventType)

			if err := stream.Decode(event.Interface()); err != nil {
				log.Printf("Closing watch due to decoding error %s", err)
				return
			}

			method := event.MethodByName("LogEvent")
			if !method.IsValid() {
				log.Println("Invalid Log Events method, skipping event logging")
			} else {
				method.Call([]reflect.Value{})
			}
		}
	}()

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

func (s *mongoStore) Name() string {
	return s.db.Name()
}

type mongoClient struct {
	StoreClient
	client *mongo.Client
}

type MongoConfig struct {
	Host     string
	Port     string
	Uri      string
	Username string
	Password string
}

func (c *MongoConfig) validate() error {
	if c.Uri != "" {
		if c.Host != "" || c.Port != "" {
			return errors.Wrap(errors.InvalidArgument, "cannot provide host and port if uri is configured")
		}
	} else {
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
	}
	return nil
}

func NewMongoClient(conf *MongoConfig) (StoreClient, error) {
	if err := conf.validate(); err != nil {
		return nil, err
	}
	// TODO(prabhjot) better to enventually switch to just uri
	// instead of allowing host port configuration
	var uri string
	if conf.Uri != "" {
		uri = conf.Uri
	} else {
		uri = "mongodb://" + net.JoinHostPort(conf.Host, conf.Port)
	}
	clientOptions := options.Client()
	clientOptions.ApplyURI(uri)
	clientOptions.SetAuth(options.Credential{
		AuthMechanism: "SCRAM-SHA-256",
		AuthSource:    "admin", //getSourceIdentifier(),
		Username:      conf.Username,
		Password:      conf.Password,
	})

	// by default ensure majority write concern and journal to be true
	// for HA to function appropriately
	//
	// while sync package might require a better consistency than just
	// majority, so we need to carefully evaluate right configuration
	// for the same
	wc := writeconcern.Majority()
	wc.Journal = utils.BoolP(true)
	clientOptions.SetWriteConcern(wc)

	client, err := mongo.Connect(clientOptions)
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
