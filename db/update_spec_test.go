// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/go-core-stack/core/errors"
)

// interpretUpdateSpecError's classes, without a server: write errors that
// mean the request does not fit the data are InvalidArgument, a write-concern
// error alone is Unavailable, and a duplicate key or any write error wins
// over a write-concern error.
func TestInterpretUpdateSpecError(t *testing.T) {
	wce := &mongo.WriteConcernError{Code: 64, Message: "waiting for replication timed out"}
	for name, tc := range map[string]struct {
		err  error
		want errors.ErrCode
	}{
		"type mismatch":                       {mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 14}}}, errors.InvalidArgument},
		"conflicting update operators":        {mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 40}}}, errors.InvalidArgument},
		"path not viable":                     {mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 28}}}, errors.InvalidArgument},
		"write concern alone":                 {mongo.WriteException{WriteConcernError: wce}, errors.Unavailable},
		"write error and write concern":       {mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 14}}, WriteConcernError: wce}, errors.InvalidArgument},
		"duplicate key first":                 {mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 11000}}, WriteConcernError: wce}, errors.AlreadyExists},
		"other write error":                   {mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 66}}}, errors.Unknown},
		"other write error and write concern": {mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 66}}, WriteConcernError: wce}, errors.Unknown},
	} {
		t.Run(name, func(t *testing.T) {
			if got := errors.GetErrCode(interpretUpdateSpecError(tc.err)); got != tc.want {
				t.Errorf("code = %v, want %v", got, tc.want)
			}
		})
	}
}

// fixedDoc's Go fields are all empty, but its pointer type's encoder writes
// a field.
type fixedDoc struct {
	S string `bson:"s,omitempty"`
}

func (d *fixedDoc) MarshalBSON() ([]byte, error) {
	return bson.Marshal(bson.D{{Key: "kind", Value: "fixed"}})
}

// blankDoc has a Go field set, but its pointer type's encoder writes nothing.
type blankDoc struct{ S string }

func (d *blankDoc) MarshalBSON() ([]byte, error) { return bson.Marshal(bson.D{}) }

// fixedList is an empty slice whose pointer type's encoder writes a field.
type fixedList []string

func (l *fixedList) MarshalBSON() ([]byte, error) {
	return bson.Marshal(bson.D{{Key: "kind", Value: "fixed"}})
}

// Each part of a spec is judged by the bytes that will be sent, and those
// bytes are what is sent: a marshaler on the pointer type runs when, and only
// when, the driver would run it for the value as given.
func TestSpecDocumentsAreJudgedByWhatIsSent(t *testing.T) {
	var typedNil bson.M
	type sparse struct {
		S string `bson:"s,omitempty"`
	}
	for _, v := range []any{nil, typedNil, bson.D{}, bson.M{}, []string{}, &bson.D{}, &typedNil, sparse{}, &sparse{}, struct{}{},
		bson.Raw{5, 0, 0, 0, 0}, &blankDoc{S: "x"}, fixedDoc{}} {
		doc, err := encodeSpecDocument(v)
		if err != nil || doc != nil {
			t.Errorf("encodeSpecDocument(%#v) = %v, %v; want nothing to send", v, doc, err)
		}
	}
	for _, v := range []any{bson.D{{Key: "a", Value: 1}}, bson.M{"a": 1}, struct{ A int }{1}, sparse{S: "x"},
		&fixedDoc{}, blankDoc{S: "x"}, &fixedList{}} {
		doc, err := encodeSpecDocument(v)
		if err != nil || doc == nil {
			t.Fatalf("encodeSpecDocument(%#v) = %v, %v; want a document", v, doc, err)
		}
		want, err := bson.Marshal(v)
		if err != nil || string(doc) != string(want) {
			t.Errorf("encodeSpecDocument(%#v) = %v; want the driver's own encoding %v", v, doc, bson.Raw(want))
		}
	}
}
