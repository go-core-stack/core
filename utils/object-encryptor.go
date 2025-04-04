// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>
// Initial reference and motivation taken from
// https://gitlab.com/project-emco/core/emco-base/-/blob/main/src/orchestrator/pkg/infra/utils/objectencryptor.go

package utils

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"log"
	"os"
	"reflect"
	"strings"
)

// IObjectEncryptor is responsible for encrypting and decrypting objects
// while transacting with an IO ensuring capability of handling secret
// fields available as part of the data. while avoiding heavy usage of
// Vaults and HSM for High Transaction interfaces
type IObjectEncryptor interface {
	// Encrypt a given object
	EncryptObject(o interface{}) (interface{}, error)

	// Encrypt a given string message
	EncryptString(message string) (string, error)

	// Decrypt an existing encrypted object
	DecryptObject(o interface{}) (interface{}, error)

	// Decrypt an existing encrypted string
	DecryptString(ciphermessage string) (string, error)
}

type myObjectEncryptor struct {
	gcm   cipher.AEAD
	nonce []byte
}

var gobjencs = make(map[string]IObjectEncryptor)

func GetObjectEncryptor(provider string) IObjectEncryptor {
	if gobjencs[provider] == nil {
		envkey := strings.ToUpper(provider) + "_DATA_KEY"
		if len(os.Getenv(envkey)) > 0 {
			oe, err := createObjectEncryptor([]byte(os.Getenv(envkey)), []byte("emco nonce"))
			if err != nil {
				log.Println("Create Object Encryptor error :: ", err)
				return nil
			}
			gobjencs[provider] = oe
		} else {
			return nil
		}
	}

	return gobjencs[provider]
}

func createObjectEncryptor(key []byte, nonce []byte) (IObjectEncryptor, error) {
	// Format key and nonce
	nkey := make([]byte, 32)
	nnonce := make([]byte, 12)
	for i := 0; i < 32; i++ {
		if i < len(key) {
			nkey[i] = key[i]
		} else {
			nkey[i] = 10
		}
	}

	for i := 0; i < 12; i++ {
		if i < len(nonce) {
			nnonce[i] = nonce[i]
		} else {
			nnonce[i] = 10
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

	return &myObjectEncryptor{aesgcm, nnonce}, nil
}

func (c *myObjectEncryptor) EncryptObject(o interface{}) (interface{}, error) {
	return c.processObject(o, false, c.EncryptString)
}

func (c *myObjectEncryptor) DecryptObject(o interface{}) (interface{}, error) {
	return c.processObject(o, false, c.DecryptString)
}

func (c *myObjectEncryptor) EncryptString(message string) (string, error) {
	ciphermessage := c.gcm.Seal(nil, c.nonce, []byte(message), nil)
	return hex.EncodeToString(ciphermessage), nil
}

func (c *myObjectEncryptor) DecryptString(ciphermessage string) (string, error) {
	cm, err := hex.DecodeString(ciphermessage)
	if err != nil {
		return "", err
	}

	message, err := c.gcm.Open(nil, c.nonce, cm, nil)

	if err != nil {
		return "", err
	}

	return string(message), nil
}

func (c *myObjectEncryptor) processObject(o interface{}, encrypt bool, oper func(string) (string, error)) (interface{}, error) {
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
