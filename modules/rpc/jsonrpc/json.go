// Copyright 2022 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"
)

const (
	vsn                      = "2.0"
	serviceMethodSeparator   = "_"
	subscribeMethodSuffix    = "_subscribe"
	notificationMethodSuffix = "_subscription"
	unsubscribeMethodSuffix  = "_unsubscribe"

	defaultWriteTimeout = 10 * time.Second
)

var null = json.RawMessage("null")

type subscriptionResult struct {
	ID     string          `json:"subscription"`
	Result json.RawMessage `json:"result,omitempty"`
}

type jsonrpcMessage struct {
	Version string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Error   *jsonError      `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

func (msg *jsonrpcMessage) isNotification() bool {
	return msg.ID == nil && msg.Method != ""
}

func (msg *jsonrpcMessage) isCall() bool {
	return msg.hasValidID() && msg.Method != ""
}

func (msg *jsonrpcMessage) isResponse() bool {
	return msg.hasValidID() && msg.Method == "" && msg.Params == nil && (msg.Result != nil || msg.Error != nil)
}

func (msg *jsonrpcMessage) hasValidID() bool {
	return len(msg.ID) > 0 && msg.ID[0] != '{' && msg.ID[0] != '['
}

func (msg *jsonrpcMessage) isSubscribe() bool {
	return strings.HasSuffix(msg.Method, subscribeMethodSuffix)
}

func (msg *jsonrpcMessage) isUnsubscribe() bool {
	return strings.HasSuffix(msg.Method, unsubscribeMethodSuffix)
}

func (msg *jsonrpcMessage) namespace() string {
	elem := strings.SplitN(msg.Method, serviceMethodSeparator, 2)
	return elem[0]
}

func (msg *jsonrpcMessage) String() string {
	b, _ := json.Marshal(msg)
	return string(b)
}

func (msg *jsonrpcMessage) errorResponse(err error) *jsonrpcMessage {
	resp := errorMessage(err)
	resp.ID = msg.ID
	return resp
}

func (msg *jsonrpcMessage) response(result interface{}) *jsonrpcMessage {
	enc, err := json.Marshal(result)
	if err != nil {
		// TODO: wrap with 'internal server error'
		return msg.errorResponse(err)
	}
	return &jsonrpcMessage{Version: vsn, ID: msg.ID, Result: enc}
}

func errorMessage(err error) *jsonrpcMessage {
	msg := &jsonrpcMessage{Version: vsn, ID: null, Error: &jsonError{
		Code:    defaultErrorCode,
		Message: err.Error(),
	}}
	ec, ok := err.(Error)
	if ok {
		msg.Error.Code = ec.ErrorCode()
	}
	de, ok := err.(DataError)
	if ok {
		msg.Error.Data = de.ErrorData()
	}
	return msg
}

type jsonError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (err *jsonError) Error() string {
	if err.Message == "" {
		return fmt.Sprintf("json-rpc error %d", err.Code)
	}
	return err.Message
}

func (err *jsonError) ErrorCode() int {
	return err.Code
}

func (err *jsonError) ErrorData() interface{} {
	return err.Data
}

type Conn interface {
	io.ReadWriteCloser
	SetWriteDeadline(time.Time) error
}

type deadlineCloser interface {
	io.Closer
	SetWriteDeadline(time.Time) error
}

type ConnRemoteAddr interface {
	RemoteAddr() string
}

type jsonCodec struct {
	remote  string
	closer  sync.Once
	closeCh chan interface{}
	decode  func(v interface{}) error
	encMu   sync.Mutex
	encode  func(v interface{}) error
	conn    deadlineCloser
}

func NewFuncCodec(conn deadlineCloser, encode, decode func(v interface{}) error) ServerCodec {
	codec := &jsonCodec{
		closeCh: make(chan interface{}),
		encode:  encode,
		decode:  decode,
		conn:    conn,
	}
	if ra, ok := conn.(ConnRemoteAddr); ok {
		codec.remote = ra.RemoteAddr()
	}
	return codec
}

func NewCodec(conn Conn) ServerCodec {
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	dec.UseNumber()
	return NewFuncCodec(conn, enc.Encode, dec.Decode)
}

func (c *jsonCodec) remoteAddr() string {
	return c.remote
}

func (c *jsonCodec) readBatch() (messages []*jsonrpcMessage, batch bool, err error) {
	// Decode the next JSON object in the input stream.
	// This verifies basic syntax, etc.
	var rawmsg json.RawMessage
	if err := c.decode(&rawmsg); err != nil {
		return nil, false, err
	}
	messages, batch = parseMessage(rawmsg)
	for i, msg := range messages {
		if msg == nil {
			// Message is JSON 'null'. Replace with zero value so it
			// will be treated like any other invalid message.
			messages[i] = new(jsonrpcMessage)
		}
	}
	return messages, batch, nil
}

func (c *jsonCodec) writeJSON(ctx context.Context, v interface{}) error {
	c.encMu.Lock()
	defer c.encMu.Unlock()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultWriteTimeout)
	}
	c.conn.SetWriteDeadline(deadline)
	return c.encode(v)
}

func (c *jsonCodec) close() {
	c.closer.Do(func() {
		close(c.closeCh)
		c.conn.Close()
	})
}

// Closed returns a channel which will be closed when Close is called
func (c *jsonCodec) closed() <-chan interface{} {
	return c.closeCh
}

func parseMessage(raw json.RawMessage) ([]*jsonrpcMessage, bool) {
	if !isBatch(raw) {
		msgs := []*jsonrpcMessage{{}}
		json.Unmarshal(raw, &msgs[0])
		return msgs, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.Token() // skip '['
	var msgs []*jsonrpcMessage
	for dec.More() {
		msgs = append(msgs, new(jsonrpcMessage))
		dec.Decode(&msgs[len(msgs)-1])
	}
	return msgs, true
}

// isBatch returns true when the first non-whitespace characters is '['
func isBatch(raw json.RawMessage) bool {
	for _, c := range raw {
		// skip insignificant whitespace (http://www.ietf.org/rfc/rfc4627.txt)
		if c == 0x20 || c == 0x09 || c == 0x0a || c == 0x0d {
			continue
		}
		return c == '['
	}
	return false
}

func parsePositionalArguments(rawArgs json.RawMessage, types []reflect.Type) ([]reflect.Value, error) {
	dec := json.NewDecoder(bytes.NewReader(rawArgs))
	var args []reflect.Value
	tok, err := dec.Token()
	switch {
	case err == io.EOF || tok == nil && err == nil:
	case err != nil:
		return nil, err
	case tok == json.Delim('['):
		if args, err = parseArgumentArray(dec, types); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("non-array args")
	}
	for i := len(args); i < len(types); i++ {
		if types[i].Kind() != reflect.Ptr {
			return nil, fmt.Errorf("missing value for required argument %d", i)
		}
		args = append(args, reflect.Zero(types[i]))
	}
	return args, nil
}

func parseArgumentArray(dec *json.Decoder, types []reflect.Type) ([]reflect.Value, error) {
	args := make([]reflect.Value, 0, len(types))
	for i := 0; dec.More(); i++ {
		if i >= len(types) {
			return args, fmt.Errorf("too many arguments, want at most %d", len(types))
		}
		argval := reflect.New(types[i])
		if err := dec.Decode(argval.Interface()); err != nil {
			return args, fmt.Errorf("invalid argument %d: %v", i, err)
		}
		if argval.IsNil() && types[i].Kind() != reflect.Ptr {
			return args, fmt.Errorf("missing value for required argument %d", i)
		}
		args = append(args, argval.Elem())
	}
	_, err := dec.Token()
	return args, err
}

func parseSubscriptionName(rawArgs json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(rawArgs))
	if tok, _ := dec.Token(); tok != json.Delim('[') {
		return "", errors.New("non-array args")
	}
	v, _ := dec.Token()
	method, ok := v.(string)
	if !ok {
		return "", errors.New("expected subscription name as first argument")
	}
	return method, nil
}
