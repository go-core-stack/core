// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package reconciler

import (
	"context"
	"log"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/Prabhjot-Sethi/core/db"
	"github.com/Prabhjot-Sethi/core/errors"
)

type MyKey struct {
	Name string
}

type MyData struct {
	Desc string
}

type MyKeyObject struct {
	Key  *MyKey `bson:"_id,omitempty"`
	Desc string
}

type MyTable struct {
	ManagerImpl
	col db.StoreCollection
}

func (t *MyTable) Callback(op string, wKey any) {
	t.NotifyCallback(wKey)
}

func (t *MyTable) ReconcilerGetAllKeys() []any {
	myKeys := []MyKeyObject{}
	keys := []any{}
	err := t.col.FindMany(context.Background(), nil, &myKeys)
	if err != nil {
		log.Panicf("MyTable: got error while fetching all keys %s", err)
	}
	for _, k := range myKeys {
		keys = append(keys, k.Key)
	}
	return []any(keys)
}

var table *MyTable

func performMongoSetup() {
	config := &db.MongoConfig{
		Host:     "localhost",
		Port:     "27017",
		Username: "root",
		Password: "password",
	}

	client, err := db.NewMongoClient(config)

	if err != nil {
		log.Printf("failed to connect to mongo DB Error: %s", err)
		return
	}

	s := client.GetDataStore("test")
	col := s.GetCollection("collection-reconciler")

	_, err = col.DeleteMany(context.Background(), bson.D{})
	if err != nil && !errors.IsNotFound(err) {
		log.Panicf("Setup: failed to cleanup existing entries: %s", err)
	}

	key := &MyKey{
		Name: "test-key-1",
	}
	data := &MyData{
		Desc: "sample-description",
	}

	err = col.InsertOne(context.Background(), key, data)
	if err != nil {
		log.Printf("failed to insert an entry to collection Error: %s", err)
	}

	table = &MyTable{}
	table.col = col
	_ = table.col.SetKeyType(reflect.TypeOf(&MyKey{}))
	_ = table.col.Watch(context.Background(), nil, table.Callback)
	_ = table.Initialize(context.Background(), table)
}

func tearDownMongoSetup() {
	_, _ = table.col.DeleteMany(context.Background(), bson.D{})
}

type MyController struct {
	Controller
	reEnqueue     bool
	retError      bool
	notifications int
}

func (c *MyController) Reconcile(k any) (*Result, error) {
	key := k.(*MyKey)
	if key.Name == "" {
		log.Panicln("Got invalid key response")
	}
	c.notifications += 1
	if c.retError {
		c.retError = false
		return nil, errors.Wrap(errors.Unknown, "test error return")
	}
	if c.reEnqueue {
		c.reEnqueue = false
		return &Result{
			RequeueAfter: 1 * time.Second,
		}, nil
	}
	return &Result{}, nil
}

func Test_ReconcilerBaseValidations(t *testing.T) {
	performMongoSetup()

	crtl := &MyController{}

	err := table.Register("test", crtl)
	if err != nil {
		t.Errorf("Got Error %s, while registering controller", err)
	}

	time.Sleep(1 * time.Second)
	if crtl.notifications != 1 {
		t.Errorf("Got %d notifications, expected only 1", crtl.notifications)
	}

	crtl.reEnqueue = true

	key := &MyKey{
		Name: "test-key-2",
	}
	data := &MyData{
		Desc: "sample-description",
	}

	err = table.col.InsertOne(context.Background(), key, data)
	if err != nil {
		log.Printf("failed to insert an entry to collection Error: %s", err)
	}

	time.Sleep(3 * time.Second)
	if crtl.notifications != 3 {
		t.Errorf("Got %d notifications, expected 3", crtl.notifications)
	}

	crtl.retError = true
	key = &MyKey{
		Name: "test-key-3",
	}
	data = &MyData{
		Desc: "sample-description",
	}

	err = table.col.InsertOne(context.Background(), key, data)
	if err != nil {
		log.Printf("failed to insert an entry to collection Error: %s", err)
	}

	time.Sleep(1 * time.Second)
	if crtl.notifications != 5 {
		t.Errorf("Got %d notifications, expected 5", crtl.notifications)
	}

	tearDownMongoSetup()
}
