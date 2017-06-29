package puppy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
)

/*
Message Server
*/

// A handler to handle a JSON message
type MessageHandler func(message []byte, conn net.Conn, logger *log.Logger, iproxy *InterceptingProxy)

// A listener that handles reading JSON messages and sending them to the correct handler
type MessageListener struct {
	handlers map[string]MessageHandler
	iproxy   *InterceptingProxy
	Logger   *log.Logger
}

type commandData struct {
	Command string
}

type errorMessage struct {
	Success bool
	Reason  string
}

// NewMessageListener creates a new message listener associated with the given intercepting proxy
func NewMessageListener(l *log.Logger, iproxy *InterceptingProxy) *MessageListener {
	m := &MessageListener{
		handlers: make(map[string]MessageHandler),
		iproxy:   iproxy,
		Logger:   l,
	}
	return m
}

// AddHandler will have the listener call the given handler when the "Command" parameter matches the given value
func (l *MessageListener) AddHandler(command string, handler MessageHandler) {
	l.handlers[strings.ToLower(command)] = handler
}

func (l *MessageListener) Handle(message []byte, conn net.Conn) error {
	var c commandData
	if err := json.Unmarshal(message, &c); err != nil {
		return fmt.Errorf("error parsing message: %s", err.Error())
	}

	handler, ok := l.handlers[strings.ToLower(c.Command)]
	if !ok {
		return fmt.Errorf("unknown command: %s", c.Command)
	}

	l.Logger.Printf("Calling handler for \"%s\"...", c.Command)
	handler(message, conn, l.Logger, l.iproxy)
	return nil
}

// Serve will have the listener serve messages on the given listener
func (l *MessageListener) Serve(nl net.Listener) {
	for {
		conn, err := nl.Accept()
		if err != nil {
			// Listener closed
			break
		}

		reader := bufio.NewReader(conn)
		go func() {
			for {
				m, err := ReadMessage(reader)
				l.Logger.Printf("> %s\n", m)
				if err != nil {
					if err != io.EOF {
						ErrorResponse(conn, "error reading message")
					}
					return
				}
				err = l.Handle(m, conn)
				if err != nil {
					ErrorResponse(conn, err.Error())
				}
			}
		}()
	}
}

// Error response writes an error message to the given writer
func ErrorResponse(w io.Writer, reason string) {
	var m errorMessage
	m.Success = false
	m.Reason = reason
	MessageResponse(w, m)
}

// MessageResponse writes a response to a given writer
func MessageResponse(w io.Writer, m interface{}) {
	b, err := json.Marshal(&m)
	if err != nil {
		panic(err)
	}
	w.Write(b)
	w.Write([]byte("\n"))
}

// ReadMessage reads a message from the given reader
func ReadMessage(r *bufio.Reader) ([]byte, error) {
	m, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	return m, nil
}
