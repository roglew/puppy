package main

import (
	"io/ioutil"
	"log"
	"sync"
)

type ConstErr string

func (e ConstErr) Error() string { return string(e) }

func DuplicateBytes(bs []byte) []byte {
	retBs := make([]byte, len(bs))
	copy(retBs, bs)
	return retBs
}

func IdCounter() func() int {
	var nextId int = 1
	var nextIdMtx sync.Mutex
	return func() int {
		nextIdMtx.Lock()
		defer nextIdMtx.Unlock()
		ret := nextId
		nextId++
		return ret
	}
}

func NullLogger() *log.Logger {
	return log.New(ioutil.Discard, "", log.Lshortfile)
}
