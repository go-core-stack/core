// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"context"
	"fmt"
	"log"
	"reflect"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type DocumentKey[K any] struct {
	Key *K `bson:"_id,omitempty"`
}

type UpdateDescription[E any] struct {
	UpdatedFields *E       `bson:"updatedFields,omitempty"`
	RemovedFields []string `bson:"removedFields,omitempty"`
}

type Namespace struct {
	Database   string `bson:"db,omitempty"`
	Collection string `bson:"coll,omitempty"`
}

type Event[K any, E any] struct {
	Doc     DocumentKey[K]        `bson:"documentKey,omitempty"`
	Op      string                `bson:"operationType,omitempty"`
	Time    bson.Timestamp        `bson:"clusterTime,omitempty"`
	Ns      *Namespace            `bson:"ns,omitempty"`
	Entry   *E                    `bson:"fullDocument,omitempty"`
	Updates *UpdateDescription[E] `bson:"updateDescription,omitempty"`
}

func (e *Event[K, E]) LogEvent() {
	msg := "Event: "
	if e.Ns != nil {
		msg += fmt.Sprintf("Coll=%s:%s, ", e.Ns.Database, e.Ns.Collection)
	}
	msg += fmt.Sprintf("Key=%v, Op=%s, Time=%v", e.Doc.Key, e.Op, e.Time)
	if e.Entry != nil {
		msg += fmt.Sprintf(", Entry= %v", *e.Entry)
	}
	if e.Updates != nil && e.Updates.UpdatedFields != nil {
		msg += fmt.Sprintf(", Updates=%v", *e.Updates.UpdatedFields)
	}

	log.Print(msg)
}

type EventLogger[K any, E any] struct {
	col StoreCollection
	ts  *bson.Timestamp
}

func NewEventLogger[K any, E any](col StoreCollection, timestamp *bson.Timestamp) *EventLogger[K, E] {
	logger := &EventLogger[K, E]{
		col: col,
		ts:  timestamp,
	}
	return logger
}

func (l *EventLogger[K, E]) Start(ctx context.Context) error {
	var event Event[K, E]
	eventType := reflect.TypeOf(event)

	log.Printf("Starting event logger for collection with event type: %s", eventType)

	return l.col.startEventLogger(ctx, eventType, l.ts)
}
