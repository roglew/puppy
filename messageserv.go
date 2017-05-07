package main

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

type MessageHandler func([]byte, net.Conn, *log.Logger, *InterceptingProxy)

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

func NewMessageListener(l *log.Logger, iproxy *InterceptingProxy) *MessageListener {
	m := &MessageListener{
		handlers: make(map[string]MessageHandler),
		iproxy:   iproxy,
		Logger:   l,
	}
	return m
}

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

func ErrorResponse(w io.Writer, reason string) {
	var m errorMessage
	m.Success = false
	m.Reason = reason
	MessageResponse(w, m)
}

func MessageResponse(w io.Writer, m interface{}) {
	b, err := json.Marshal(&m)
	if err != nil {
		panic(err)
	}
	MainLogger.Printf("< %s\n", string(b))
	w.Write(b)
	w.Write([]byte("\n"))
}

func ReadMessage(r *bufio.Reader) ([]byte, error) {
	m, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	return m, nil
}
