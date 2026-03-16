// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Initial reference and motivation taken from
// https://gitlab.com/project-emco/core/emco-base/-/blob/main/src/orchestrator/pkg/infra/utils/objectencryptor.go

package utils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"reflect"
	"sync"

	"github.com/go-core-stack/core/errors"
)

// legacyStaticNonce is the nonce that was previously used for all
// encryptions. Retained only so that DecryptString can fall back to
// it when decrypting data written before the random-nonce migration.
var legacyStaticNonce = []byte("core nonce")

// IOEncryptor is responsible for encrypting and decrypting objects
// while transacting with an IO ensuring capability of handling secret
// fields available as part of the data. while avoiding heavy usage of
// Vaults and HSM for High Transaction interfaces
type IOEncryptor interface {
	// Encrypt a given object
	EncryptObject(o interface{}) (interface{}, error)

	// Encrypt a given string message
	EncryptString(message string) (string, error)

	// Decrypt an existing encrypted object
	DecryptObject(o interface{}) (interface{}, error)

	// Decrypt an existing encrypted string
	DecryptString(ciphermessage string) (string, error)
}

// encryptor implementation
type encryptorImpl struct {
	gcm         cipher.AEAD
	legacyNonce []byte // used only for decrypting pre-migration data
}

// map to hold encryptors for different providers
var encryptors = make(map[string]IOEncryptor)

// Read Write mutex to access the above map, as we may have multiple
// go routines working together to access this library, ensuring
// thread safety
var encsLock sync.RWMutex

func GetObjectEncryptor(provider string) (IOEncryptor, error) {
	encsLock.RLock()
	defer encsLock.RUnlock()

	enc, ok := encryptors[provider]
	if !ok {
		err := errors.Wrap(errors.NotFound, "Encryptor not found")
		return nil, err
	}

	return enc, nil
}

// InitializeEncryptor initialize a new Encryptor for given
// provider, this will return an error if encryptor already
// exists
func InitializeEncryptor(provider, key string) (IOEncryptor, error) {
	// ensure taking a write lock before processing this further
	// to ensure thread safety along with appropriate error
	// handling
	encsLock.Lock()
	defer encsLock.Unlock()
	enc := encryptors[provider]
	if enc != nil {
		return nil, errors.Wrap(errors.AlreadyExists, "Encryptor Already exists")
	}

	if len(key) <= 0 {
		return nil, errors.Wrap(errors.InvalidArgument, "Invalid Key length")
	}

	oe, err := createEncryptor([]byte(key))
	if err != nil {
		return nil, errors.Wrap(errors.Unknown, "Create Object Encryptor error : "+err.Error())
	}
	encryptors[provider] = oe

	return oe, nil
}

func createEncryptor(key []byte) (IOEncryptor, error) {
	// Pad or truncate key to 32 bytes (AES-256).
	nkey := make([]byte, 32)
	for i := 0; i < 32; i++ {
		if i < len(key) {
			nkey[i] = key[i]
		} else {
			nkey[i] = 10
		}
	}

	// Build the legacy static nonce so old data can still be decrypted.
	legNonce := make([]byte, 12)
	for i := 0; i < 12; i++ {
		if i < len(legacyStaticNonce) {
			legNonce[i] = legacyStaticNonce[i]
		} else {
			legNonce[i] = 10
		}
	}

	block, err := aes.NewCipher(nkey)
	if err != nil {
		return nil, err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &encryptorImpl{gcm: aesgcm, legacyNonce: legNonce}, nil
}

func (c *encryptorImpl) EncryptObject(o interface{}) (interface{}, error) {
	return c.processObject(o, false, c.EncryptString)
}

func (c *encryptorImpl) DecryptObject(o interface{}) (interface{}, error) {
	return c.processObject(o, false, c.DecryptString)
}

func (c *encryptorImpl) EncryptString(message string) (string, error) {
	nonceSize := c.gcm.NonceSize()
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	// Seal appends ciphertext to nonce, producing: nonce || ciphertext.
	ciphermessage := c.gcm.Seal(nonce, nonce, []byte(message), nil)
	return hex.EncodeToString(ciphermessage), nil
}

func (c *encryptorImpl) DecryptString(ciphermessage string) (string, error) {
	cm, err := hex.DecodeString(ciphermessage)
	if err != nil {
		return "", err
	}

	nonceSize := c.gcm.NonceSize()

	// New format: nonce is prepended to the ciphertext.
	if len(cm) > nonceSize {
		nonce, ct := cm[:nonceSize], cm[nonceSize:]
		message, err := c.gcm.Open(nil, nonce, ct, nil)
		if err == nil {
			return string(message), nil
		}
	}

	// Legacy fallback: data encrypted with the old static nonce
	// (no prepended nonce — entire payload is ciphertext).
	message, err := c.gcm.Open(nil, c.legacyNonce, cm, nil)
	if err != nil {
		return "", err
	}

	return string(message), nil
}

func (c *encryptorImpl) processObject(o interface{}, encrypt bool, oper func(string) (string, error)) (interface{}, error) {
	t := reflect.TypeOf(o)
	switch t.Kind() {
	case reflect.String:
		// only support do encryption on string field
		if encrypt {
			val, err := oper(o.(string))
			if err != nil {
				return nil, err
			}

			return val, nil
		}
	case reflect.Ptr:
		v := reflect.ValueOf(o)
		newv, err := c.processObject(v.Elem().Interface(), encrypt, oper)
		if err != nil {
			return nil, err
		}
		v.Elem().Set(reflect.ValueOf(newv))
		return o, nil
	case reflect.Struct:
		v := reflect.ValueOf(&o).Elem()
		newv := reflect.New(v.Elem().Type()).Elem()
		newv.Set(v.Elem())
		for k := 0; k < t.NumField(); k++ {
			_, fieldEncrypt := t.Field(k).Tag.Lookup("encrypted")
			isEncrypt := fieldEncrypt || encrypt
			if t.Field(k).IsExported() {
				newf, err := c.processObject(newv.Field(k).Interface(), isEncrypt, oper)
				if err != nil {
					return nil, err
				}
				newv.Field(k).Set(reflect.ValueOf(newf))
			}
		}
		return newv.Interface(), nil
	case reflect.Array:
		v := reflect.ValueOf(o)
		newv := reflect.New(t).Elem()
		for k := 0; k < v.Len(); k++ {
			newf, err := c.processObject(v.Index(k).Interface(), encrypt, oper)
			if err != nil {
				return nil, err
			}
			newv.Index(k).Set(reflect.ValueOf(newf))
		}
		return newv.Interface(), nil
	case reflect.Slice:
		v := reflect.ValueOf(o)
		newv := reflect.MakeSlice(t, v.Len(), v.Len())
		for k := 0; k < v.Len(); k++ {
			newf, err := c.processObject(v.Index(k).Interface(), encrypt, oper)
			if err != nil {
				return nil, err
			}
			newv.Index(k).Set(reflect.ValueOf(newf))
		}
		return newv.Interface(), nil
	case reflect.Map:
		v := reflect.ValueOf(o)
		newv := reflect.MakeMap(t)
		for _, k := range v.MapKeys() {
			newf, err := c.processObject(v.MapIndex(k).Interface(), encrypt, oper)
			if err != nil {
				return nil, err
			}
			newv.SetMapIndex(k, reflect.ValueOf(newf))
		}
		return newv.Interface(), nil
	default:
	}

	return o, nil
}
